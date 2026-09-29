// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"sync/atomic"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"

	"unikraft.com/x/stdio"
)

const (
	// BuiltinMarker is what marks a word as a builtin: ":start", not "start".
	BuiltinMarker = ":"

	// helpBuiltin is the one builtin the session knows the name of, to point at
	// when a caller has one and the user asked for something else.
	helpBuiltin = "help"
)

const (
	// statusBuiltinNotFound is what the shell reports for an unknown command.
	statusBuiltinNotFound = 127

	// StatusInterrupted is what a command killed by a signal reports.
	StatusInterrupted = 130

	// statusNotRun is what a line the shell refuses to run reports.
	statusNotRun = 2

	// maxSignal is the highest signal number an exit code can encode.
	maxSignal = 64

	// maxCapture is how much of a command substitution the shell will hold.
	maxCapture = 1 << 23
)

var (
	errCaptureFull = errors.New("the command substitution is full")

	captureFull = fmt.Sprintf("command substitution kept more than %d MiB", maxCapture>>20)
)

var sessionBuiltinNames = []string{"history"}

// route sends a ":"-marked word to a builtin, which runs locally, and every other
// command to the instance.
func (s *state) route(_ interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		s.forgetStats()
		if len(args[0]) > len(BuiltinMarker) && strings.HasPrefix(args[0], BuiltinMarker) {
			return s.runBuiltin(ctx, args)
		}
		return s.runRemote(ctx, args)
	}
}

func (s *state) runRemote(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)

	ctx, done := context.WithCancel(ctx)
	defer done()

	streams := stdio.Stdio{Stdin: hc.Stdin, Stdout: hc.Stdout, Stderr: hc.Stderr}
	in, reclaim := s.commandStdin(ctx, streams.Stdin)
	streams.Stdin = in
	// Hand the terminal back before the prompt reads it again, not whenever the
	// context's watcher gets around to it.
	defer reclaim()

	out, capture := capturing(streams.Stdout, done)
	streams.Stdout = out

	code, err := s.cfg.Transport.Exec(ctx, Command{
		Args:    args,
		Dir:     hc.Dir,
		Env:     envList(hc.Env),
		Streams: streams,
	})
	switch {
	case capture.filled():
		fmt.Fprintln(hc.Stderr, errorStyle.Render(captureFull))
		return interp.ExitStatus(1)
	case err != nil && ctx.Err() != nil:
		return interp.ExitStatus(StatusInterrupted)
	case err != nil:
		fmt.Fprintln(hc.Stderr, errorStyle.Render(sanitised(err).Error()))
		return interp.ExitStatus(1)
	}
	return codeToExitStatus(code)
}

// capturing is the interpreter keeping a command's output in memory, as it does
// for $(...) and nowhere else: a pipe or a file answers for its own size.
func capturing(w io.Writer, stop context.CancelFunc) (io.Writer, *cappedCapture) {
	held, ok := w.(*strings.Builder)
	if !ok {
		return w, nil
	}
	capture := &cappedCapture{held: held, stop: stop}
	return capture, capture
}

// cappedCapture holds a command substitution to [maxCapture], however many
// commands write into it, and stops the one that filled it.
type cappedCapture struct {
	held *strings.Builder
	stop context.CancelFunc
	full atomic.Bool
}

func (c *cappedCapture) filled() bool { return c != nil && c.full.Load() }

func (c *cappedCapture) Write(p []byte) (int, error) {
	room := maxCapture - c.held.Len()
	if len(p) <= room {
		return c.held.Write(p)
	}

	c.full.Store(true)
	c.stop()
	if room <= 0 {
		return 0, errCaptureFull
	}
	n, _ := c.held.Write(p[:room])
	return n, errCaptureFull
}

func (s *state) runBuiltin(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)

	ctx, done := context.WithCancel(ctx)
	defer done()

	streams := stdio.Stdio{Stdin: hc.Stdin, Stdout: hc.Stdout, Stderr: hc.Stderr}
	out, capture := capturing(streams.Stdout, done)
	streams.Stdout = out

	err := s.answerBuiltin(ctx, streams, args)
	if capture.filled() {
		fmt.Fprintln(hc.Stderr, errorStyle.Render(captureFull))
		return interp.ExitStatus(1)
	}
	return err
}

func (s *state) answerBuiltin(ctx context.Context, streams stdio.Stdio, args []string) error {
	args = append([]string{strings.TrimPrefix(args[0], BuiltinMarker)}, args[1:]...)
	if slices.Contains(sessionBuiltinNames, args[0]) {
		return s.runSessionBuiltin(streams, args)
	}

	builtin, ok := s.cfg.Builtins[args[0]]
	if !ok && args[0] == helpBuiltin {
		// Nobody else lists builtins, so the session lists its own.
		s.printSessionBuiltins(streams.Stdout)
		return nil
	}
	if !ok {
		fmt.Fprintln(streams.Stderr, errorStyle.Render(unknownBuiltin(args[0])))
		return interp.ExitStatus(statusBuiltinNotFound)
	}

	code, err := builtin.Run(ctx, streams, args)
	if err != nil {
		fmt.Fprintln(streams.Stderr, errorStyle.Render(err.Error()))
		if code == 0 {
			code = 1
		}
	}
	if args[0] == helpBuiltin && err == nil {
		s.printSessionBuiltins(streams.Stdout)
	}
	return codeToExitStatus(code)
}

func unknownBuiltin(name string) string {
	return fmt.Sprintf("unknown builtin %q; try %s%s", name, BuiltinMarker, helpBuiltin)
}

func (s *state) runSessionBuiltin(streams stdio.Stdio, args []string) error {
	switch args[0] {
	case "history":
		if s.history == nil {
			fmt.Fprintln(streams.Stderr, errorStyle.Render("history: only at the prompt"))
			return interp.ExitStatus(1)
		}
		for i, line := range s.history.recalled() {
			fmt.Fprintf(streams.Stdout, "%5d  %s\n", i+1, line)
		}
	}
	return nil
}

func (s *state) printSessionBuiltins(out io.Writer) {
	fmt.Fprintf(out, "  %-34s %s\n", BuiltinMarker+"history", "List what this session has run.")
	fmt.Fprintln(out, "\nEverything else runs on the instance.")
}

func resolve(dir, p string) string {
	if !path.IsAbs(p) {
		p = path.Join(dir, p)
	}
	return path.Clean(p)
}

// commandStdin lends the session's input to one command at a time
func (s *state) commandStdin(ctx context.Context, in io.Reader) (io.Reader, func()) {
	if s.stdin != nil && in == io.Reader(s.stdin) {
		return lendFile(ctx, s.stdin)
	}
	return in, func() {}
}

func envList(env expand.Environ) []string {
	var vars []string
	env.Each(func(name string, vr expand.Variable) bool {
		if vr.Exported && vr.IsSet() {
			vars = append(vars, name+"="+vr.String())
		}
		return true
	})
	slices.Sort(vars)
	return vars
}

func codeToExitStatus(code int) error {
	switch {
	case code == 0:
		return nil
	case code < 0 && code >= -maxSignal:
		return interp.ExitStatus(128 - code)
	case code < 0 || code > 255:
		return interp.ExitStatus(1)
	default:
		return interp.ExitStatus(code)
	}
}
