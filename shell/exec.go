// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
)

const BuiltinMarker = ":"

const (
	// statusBuiltinNotFound is what the shell reports for an unknown command.
	statusBuiltinNotFound = 127

	// StatusInterrupted is what a command killed by a signal reports.
	StatusInterrupted = 130

	// maxSignal is the highest signal number an exit code can encode.
	maxSignal = 64
)

// route sends a ":"-marked word to a builtin, which runs locally, and every other
// command to the instance.
func (s *session) route(_ interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		if len(args[0]) > len(BuiltinMarker) && strings.HasPrefix(args[0], BuiltinMarker) {
			return s.runBuiltin(ctx, args)
		}
		return s.runRemote(ctx, args)
	}
}

func (s *session) runRemote(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)

	ctx, done := context.WithCancel(ctx)
	defer done()

	streams := Streams{In: hc.Stdin, Out: hc.Stdout, Err: hc.Stderr}
	streams.In = s.commandStdin(ctx, streams.In)

	code, err := s.cfg.Transport.Exec(ctx, streams, hc.Dir, envToMap(hc.Env), args)
	if err != nil {
		if ctx.Err() != nil {
			return interp.ExitStatus(StatusInterrupted)
		}
		fmt.Fprintln(hc.Stderr, errorStyle.Render(err.Error()))
		return interp.ExitStatus(1)
	}
	return codeToExitStatus(code)
}

func (s *session) runBuiltin(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	streams := Streams{In: hc.Stdin, Out: hc.Stdout, Err: hc.Stderr}

	args = append([]string{strings.TrimPrefix(args[0], BuiltinMarker)}, args[1:]...)
	if s.cfg.Builtins == nil {
		fmt.Fprintln(hc.Stderr, errorStyle.Render("unknown builtin: "+args[0]))
		return interp.ExitStatus(statusBuiltinNotFound)
	}

	code, err := s.cfg.Builtins.Run(ctx, streams, args)
	if err != nil {
		fmt.Fprintln(hc.Stderr, errorStyle.Render(err.Error()))
		if code == 0 {
			code = 1
		}
	}
	return codeToExitStatus(code)
}

func resolve(dir, p string) string {
	if !path.IsAbs(p) {
		p = path.Join(dir, p)
	}
	return path.Clean(p)
}

func (s *session) commandStdin(ctx context.Context, in io.Reader) io.Reader {
	switch {
	case in != nil:
		return in
	case s.tty != nil:
		return terminalStdin(ctx, s.tty)
	default:
		return nil
	}
}

func envToMap(env expand.Environ) map[string]string {
	vars := map[string]string{}
	env.Each(func(name string, vr expand.Variable) bool {
		if vr.Exported && vr.IsSet() {
			vars[name] = vr.String()
		}
		return true
	})
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
