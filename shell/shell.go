// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package shell runs a shell session whose commands, files and environment come
// from a [Transport] rather than from this machine.
package shell

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"

	xio "unikraft.com/x/io"
	"unikraft.com/x/log"
)

const (
	instanceProbeTimeout  = 3 * time.Second
	instanceProbeAttempts = 2
)

// environFallbacks fill in what an instance did not export. PATH is what a
// container image starts with when it sets none (Debian's ENV_SUPATH), so a
// shell finds what the image's own processes find; HOME and the ids are an
// instance's reality: one user, root, at the root of its filesystem.
var environFallbacks = map[string]string{
	"HOME": "/",
	"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	"UID":  "0",
	"EUID": "0",
	"GID":  "0",
}

var (
	errNotATerminal = errors.New("the shell needs a terminal; use -c '<command line>' to run commands without one")
	errNoPrompt     = errors.New("the interactive prompt is not in this build; use -c '<command line>'")
)

// console is where the session writes, serialised so two commands cannot interleave a write.
type console struct {
	Out, Err io.Writer
}

type session struct {
	cfg     Config
	console console

	runner *interp.Runner

	interactive bool

	// noShell is set when the probe found no sh on the instance: commands still
	// run by argv, but nothing that needs a shell over there can.
	noShell bool

	tty *os.File
}

func Run(ctx context.Context, cfg Config, streams Streams) (int, error) {
	if cfg.Transport == nil {
		return 0, fmt.Errorf("no transport to the instance")
	}
	if cfg.Dir == "" {
		cfg.Dir = "/"
	}

	s := &session{cfg: cfg}
	if f, ok := streams.In.(*os.File); ok && xio.IsTTYReader(f) {
		s.tty = f
	}
	s.interactive = cfg.Command == "" && s.tty != nil && xio.IsTTY(streams.Out)

	var terminal sync.Mutex
	s.console = console{
		Out: &lockedWriter{mu: &terminal, w: streams.Out},
		Err: &lockedWriter{mu: &terminal, w: streams.Err},
	}

	env, err := s.environ(ctx)
	if err != nil {
		return 0, err
	}

	runner, err := interp.New(
		interp.StdIO(streams.In, s.console.Out, s.console.Err),
		interp.Env(expand.ListEnviron(env...)),
		interp.Interactive(s.interactive),
		interp.CallHandler(keepLookupsRemote),
		interp.ExecHandlers(s.route),
		interp.StatHandler(s.statHandler),
		interp.AccessHandler(s.accessHandler),
		interp.ReadDirHandler2(s.readDirHandler),
		interp.OpenHandler(s.open),
	)
	if err != nil {
		return 0, err
	}

	runner.Dir = cfg.Dir
	runner.Reset()
	s.runner = runner

	switch {
	case cfg.Command != "":
		return s.runSource(ctx, strings.NewReader(cfg.Command))
	case s.interactive:
		return 0, errNoPrompt
	default:
		return 0, errNotATerminal
	}
}

func (s *session) runSource(ctx context.Context, src io.Reader) (int, error) {
	prog, err := syntax.NewParser().Parse(src, "")
	if err != nil {
		return 0, err
	}
	if err := unsupported(prog); err != nil {
		return 0, err
	}
	return exitStatus(s.runner.Run(ctx, prog))
}

// exitStatus separates a command's exit status from the interpreter failing.
func exitStatus(err error) (int, error) {
	if status, ok := errors.AsType[interp.ExitStatus](err); ok {
		return int(status), nil
	}
	return 0, err
}

// unsupported finds what the interpreter would have to answer from this machine
// rather than the instance
func unsupported(prog *syntax.File) error {
	var err error
	syntax.Walk(prog, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.ProcSubst:
			err = fmt.Errorf("%s: process substitution is not supported, the pipe would be on this machine", n.Pos())
		case *syntax.Word:
			if len(n.Parts) == 0 {
				break
			}
			// A word opening with ~ and a name: bare ~ and ~/ are $HOME, and fine.
			if lit, ok := n.Parts[0].(*syntax.Lit); ok && len(lit.Value) > 1 && lit.Value[0] == '~' &&
				(lit.Value[1] == '_' || unicode.IsLetter(rune(lit.Value[1]))) {
				err = fmt.Errorf("%s: ~user is not supported, it would be this machine's user", n.Pos())
			}
		}
		return err == nil
	})
	return err
}

// keepLookupsRemote stops type, command -v and pwd -P from running locally
func keepLookupsRemote(_ context.Context, args []string) ([]string, error) {
	var local bool
	switch args[0] {
	case "type":
		local = true
	case "command":
		local = len(args) > 1 && (args[1] == "-v" || args[1] == "-V")
	case "pwd":
		local = slices.Contains(args[1:], "-P")
	}
	if !local {
		return args, nil
	}
	return append([]string{"sh", "-c", args[0] + ` "$@"`, "sh"}, args[1:]...), nil
}

func (s *session) environ(ctx context.Context) ([]string, error) {
	out, err := s.probe(ctx)
	if err != nil {
		log.G(ctx).Debug().Err(err).Msg("could not read the instance environment")

		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("could not reach %s: %w", cmp.Or(s.cfg.Instance, "the instance"), err)
	}

	vars := map[string]string{}
	for line := range strings.SplitSeq(out, "\n") {
		if name, value, ok := strings.Cut(line, "="); ok && isEnvName(name) {
			vars[name] = value
		}
	}
	maps.Copy(vars, s.cfg.Env)

	for name, fallback := range environFallbacks {
		if vars[name] == "" {
			vars[name] = fallback
		}
	}

	env := make([]string, 0, len(vars))
	for name, value := range vars {
		env = append(env, name+"="+value)
	}
	slices.Sort(env)
	return env, nil
}

func (s *session) probe(ctx context.Context) (string, error) {
	var err error
	for range instanceProbeAttempts {
		var out bytes.Buffer

		probeCtx, cancel := context.WithTimeout(ctx, instanceProbeTimeout)
		probeCtx = Detached(probeCtx)

		var code int
		code, err = s.cfg.Transport.Exec(probeCtx, Streams{Out: &out, Err: io.Discard}, s.cfg.Dir, nil,
			[]string{"sh", "-c", environProbe})
		cancel()

		switch {
		case err == nil:
			s.noShell = code == statusBuiltinNotFound
			return out.String(), nil
		case ctx.Err() != nil:
			return "", err
		}
	}
	return "", err
}

func isEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// lockedWriter serialises writes from commands running at the same time.
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
