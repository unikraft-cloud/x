// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"context"
	"fmt"
	"syscall"

	"unikraft.com/x/stdio"
)

// Session is a shell a caller drives a line at a time, bringing its own line
// editing
type Session struct {
	s *state
}

// New opens a session the caller drives a line at a time. Wrap streams.Stdout in a
// *colorprofile.Writer: it is what brings the prompt and a command's own
// colours down to what the far end can show, and without one the profile is
// this process' environment, a server's and not a terminal's.
func New(ctx context.Context, cfg Config, streams stdio.Stdio) (*Session, error) {
	if cfg.Command != "" {
		return nil, fmt.Errorf("a session runs the lines it is given, not cfg.Command")
	}
	if streams.Stdin != nil {
		return nil, fmt.Errorf("a session reads keystrokes from cfg.Input, not streams.Stdin")
	}

	s, err := newState(ctx, cfg, streams)
	if err != nil {
		return nil, err
	}
	s.interactive = true
	s.history = &sessionHistory{}

	return &Session{s: s}, nil
}

func (s *Session) RunLine(ctx context.Context, line string) (int, error) {
	status, _, err := s.s.runInput(ctx, line)
	return status, err
}

func (s *Session) Exited() bool {
	return s.s.runner.Exited() && s.s.exiting.Load()
}

func (s *Session) Interrupt() {
	select {
	case s.s.interrupts <- syscall.SIGINT:
	default:
	}
}

// Close ends the session, running the EXIT trap that ^D runs at a terminal, and
// releases what it holds. Call it once however the session ended, not only on a
// ^D; the trap's own status is the session's to swallow.
func (s *Session) Close(ctx context.Context) error {
	_ = s.s.runner.Run(ctx, sessionEnd)
	return nil
}

func (s *Session) Dir() string { return s.s.dir() }

func (s *Session) Prompt(continuation bool) string { return s.s.prompt(continuation) }

func (s *Session) Highlight(line string) string {
	return s.s.paint(highlight(line, s.s.isBuiltinName))
}

func AcceptMultiline(line []rune) bool { return acceptMultiline(line) }
