// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"context"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

const blockingCommand = "block"

// haltingTransport stands in for a command that only stops when it is told to,
// and reports the status the instance would
type haltingTransport struct {
	started  chan struct{}
	starting sync.Once

	mu       sync.Mutex
	ran      []string
	detached []bool
}

func newHaltingTransport() *haltingTransport {
	return &haltingTransport{started: make(chan struct{})}
}

func (t *haltingTransport) Exec(ctx context.Context, cmd Command) (int, error) {
	t.mu.Lock()
	t.ran = append(t.ran, strings.Join(cmd.Args, " "))
	t.detached = append(t.detached, IsDetached(ctx))
	t.mu.Unlock()

	if cmd.Args[0] != blockingCommand {
		return 0, nil
	}
	t.starting.Do(func() { close(t.started) })

	select {
	case <-ctx.Done():
		return -int(syscall.SIGINT), nil
	case <-time.After(10 * time.Second):
		return 0, nil
	}
}

func (t *haltingTransport) commands() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.ran...)
}

// lastDetached is whether the last command asked of the transport was detached
func (t *haltingTransport) lastDetached(tt *testing.T) bool {
	tt.Helper()
	t.mu.Lock()
	defer t.mu.Unlock()
	require.NotEmpty(tt, t.detached, "the transport was never asked anything")
	return t.detached[len(t.detached)-1]
}

func newHaltingSession(t *testing.T, transport *haltingTransport) (*state, *captured) {
	t.Helper()

	out := &captured{}
	s := &state{
		cfg:        Config{Transport: ExecTransport(transport.Exec)},
		console:    console{Out: out, Err: out},
		interrupts: make(chan os.Signal, 4),
	}

	runner, err := interp.New(
		interp.StdIO(nil, out, out),
		interp.CallHandler(s.call),
		interp.ExecHandlers(s.route),
	)
	require.NoError(t, err)

	runner.Dir = "/"
	runner.Reset()
	s.runner = runner
	return s, out
}

func parseShell(t *testing.T, src string) []*syntax.Stmt {
	t.Helper()
	prog, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	require.NoError(t, err)
	return prog.Stmts
}

func interruptOnce(t *haltingTransport, sigint chan os.Signal) {
	go func() {
		<-t.started
		sigint <- syscall.SIGINT
	}()
}

func runShellLine(t *testing.T, s *state, src string) bool {
	t.Helper()

	for _, stmt := range parseShell(t, src) {
		_, interrupted, err := s.runStmt(t.Context(), stmt)
		require.NoError(t, err)
		if interrupted {
			return true
		}
	}
	return false
}

func status(t *testing.T, s *state, out *captured) string {
	t.Helper()

	before := len(out.String())
	_, err := interpStatus(s.runner.Run(t.Context(), &syntax.File{
		Stmts: parseShell(t, `printf 'status=%s' "$?"`),
	}))
	require.NoError(t, err)
	reported, found := strings.CutPrefix(out.String()[before:], "status=")
	require.True(t, found, "printf did not report the status")
	return reported
}

func TestInterruptStopsTheCommand(t *testing.T) {
	transport := newHaltingTransport()
	s, out := newHaltingSession(t, transport)

	interruptOnce(transport, s.interrupts)

	assert.True(t, runShellLine(t, s, blockingCommand))
	assert.Equal(t, "130", status(t, s, out))
}

func TestInterruptKeepsTheSession(t *testing.T) {
	for _, tt := range []struct {
		name string
		line string
	}{
		{"while-loop", "while true; do " + blockingCommand + "; done"},
		{"block", "{ " + blockingCommand + "; echo unreachable; }"},
		{"function-body", "f() { " + blockingCommand + "; echo unreachable; }; f"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			transport := newHaltingTransport()
			s, out := newHaltingSession(t, transport)

			interruptOnce(transport, s.interrupts)

			assert.True(t, runShellLine(t, s, tt.line))
			assert.False(t, s.runner.Exited(), "the shell must survive its own Ctrl-C")
			assert.Equal(t, "130", status(t, s, out))
			assert.NotContains(t, out.String(), "unreachable")
		})
	}
}

func TestAnInterruptBetweenStatementsIsNotLost(t *testing.T) {
	s, _ := newHaltingSession(t, newHaltingTransport())

	// What a ^C typed once a statement has ended, but before the next has
	// started, leaves behind: nothing is watching the channel just then.
	s.interrupts <- syscall.SIGINT

	_, interrupted, err := s.runStmt(t.Context(), parseShell(t, "echo next")[0])
	require.NoError(t, err)
	assert.True(t, interrupted, "the next statement answers the line's ^C")
}

func TestALineDropsOnlyWhatWasTypedBeforeIt(t *testing.T) {
	transport := newHaltingTransport()
	s, _ := newHaltingSession(t, transport)

	// Left over from before the line: the prompt took the ^C itself.
	s.interrupts <- syscall.SIGINT

	_, exited := s.runFile(t.Context(), &syntax.File{Stmts: parseShell(t, "first; second")})

	assert.False(t, exited)
	assert.Equal(t, []string{"first", "second"}, transport.commands(),
		"a stale ^C is dropped once, not once per statement")
}

func TestInterruptAbandonsTheLine(t *testing.T) {
	transport := newHaltingTransport()
	s, _ := newHaltingSession(t, transport)

	interruptOnce(transport, s.interrupts)

	assert.True(t, runShellLine(t, s, blockingCommand+"; second"))
	assert.Equal(t, []string{blockingCommand}, transport.commands())
}

func TestRepeatedInterruptsStillStopTheCommand(t *testing.T) {
	transport := newHaltingTransport()
	s, out := newHaltingSession(t, transport)

	go func() {
		<-transport.started
		for range 3 {
			s.interrupts <- syscall.SIGINT
		}
	}()

	assert.True(t, runShellLine(t, s, blockingCommand))
	assert.Equal(t, "130", status(t, s, out))
}

func TestOnlyAnExitEndsTheSession(t *testing.T) {
	for _, tt := range []struct {
		name   string
		line   string
		exited bool
		status int
		want   string
	}{
		{"exit", "exit 3", true, 3, ""},
		{"exit-in-a-function", "f() { exit 4; }; f; echo unreachable", true, 4, ""},
		{"exit-in-a-subshell", "(exit 5); echo still here", false, 0, "still here\n"},
		{"unset-parameter", "echo ${nope:?is not set}; echo still here", false, 0, "nope: is not set\nstill here\n"},
		{"errexit", "set -e; false; echo still here", false, 0, "still here\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, out := newHaltingSession(t, newHaltingTransport())
			status, exited := s.runFile(t.Context(), &syntax.File{Stmts: parseShell(t, tt.line)})

			assert.Equal(t, tt.exited, exited)
			assert.Equal(t, tt.status, status)
			assert.Equal(t, tt.want, out.String())

			out.Reset()
			_, exited = s.runFile(t.Context(), &syntax.File{Stmts: parseShell(t, "echo next")})
			assert.False(t, exited)
			assert.Equal(t, "next\n", out.String(), "the session goes on")
		})
	}
}
