// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A command with the terminal to itself runs on a pty of its size, and the pty's ^C ends the line.

// terminalTransport records each command's terminal, and runs the sh helpers so redirections open.
type terminalTransport struct {
	mu   sync.Mutex
	seen map[string]*Terminal

	// gate holds a running command until the test lets it read the next size.
	running chan *Terminal
	gate    chan struct{}
	sizes   chan WinSize
}

func (t *terminalTransport) Exec(ctx context.Context, cmd Command) (int, error) {
	if cmd.Args[0] == "sh" {
		return local().Exec(ctx, cmd)
	}

	t.mu.Lock()
	if t.seen == nil {
		t.seen = map[string]*Terminal{}
	}
	t.seen[cmd.Args[0]] = cmd.Terminal
	t.mu.Unlock()

	if t.running != nil {
		t.running <- cmd.Terminal
		<-t.gate
		select {
		case size := <-cmd.Terminal.Resize:
			t.sizes <- size
		case <-ctx.Done():
		}
	}
	return 0, nil
}

func (t *terminalTransport) terminal(name string) *Terminal {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.seen[name]
}

func TestATerminalCommandHasTheTerminalToItself(t *testing.T) {
	root := newFixture(t)
	transport := &terminalTransport{}
	s, _, _ := newDrivenSession(t, Config{Instance: "fake", Dir: root, Transport: ExecTransport(transport.Exec)})
	s.Resize(120, 24)

	for _, line := range []string{
		"alone",
		"( grouped )",
		"left | right",
		"redirected > " + filepath.Join(root, "out.txt"),
		"echo $(captured)",
		"quiet 2>/dev/null",
		"merged 2>&1",
		"fed < " + filepath.Join(root, "hostname"),
	} {
		_, err := s.RunLine(t.Context(), line)
		require.NoError(t, err, line)
	}

	want := &Terminal{Size: WinSize{Cols: 120, Rows: 24}}
	for name, terminal := range map[string]*Terminal{
		"alone":      want,
		"grouped":    want,
		"merged":     want,
		"left":       nil,
		"right":      nil,
		"redirected": nil,
		"captured":   nil,
		"quiet":      nil,
		"fed":        nil,
	} {
		got := transport.terminal(name)
		if terminal == nil {
			assert.Nil(t, got, name)
			continue
		}
		require.NotNil(t, got, name)
		assert.Equal(t, terminal.Size, got.Size, name)
		assert.NotNil(t, got.Resize, name)
	}
	assert.Nil(t, transport.terminal("sh"), "a helper never has the terminal")
}

func TestTheNewestSizeReachesTheCommand(t *testing.T) {
	transport := &terminalTransport{running: make(chan *Terminal), gate: make(chan struct{}), sizes: make(chan WinSize, 1)}
	s, _, _ := newDrivenSession(t, Config{Instance: "fake", Dir: newFixture(t), Transport: ExecTransport(transport.Exec)})
	s.Resize(80, 25)

	done := make(chan error, 1)
	go func() {
		_, err := s.RunLine(t.Context(), "alone")
		done <- err
	}()

	terminal := <-transport.running
	assert.Equal(t, WinSize{Cols: 80, Rows: 25}, terminal.Size)

	s.Resize(100, 30)
	s.Resize(132, 43)
	close(transport.gate)

	select {
	case size := <-transport.sizes:
		assert.Equal(t, WinSize{Cols: 132, Rows: 43}, size, "a size nobody read yet is dropped for the newest")
	case <-time.After(5 * time.Second):
		t.Fatal("the resize never reached the command")
	}
	require.NoError(t, <-done)
}

func TestACtrlCOnThePtyEndsTheLine(t *testing.T) {
	root := newFixture(t)

	for _, tt := range []struct {
		name   string
		sized  bool
		code   int
		line   string
		want   string
		status int
	}{
		{"a-loop-ends", true, -2, "while true; do dies; done; echo unreachable", "\n", StatusInterrupted},
		{"a-command-that-handled-its-own-ctrl-c-lets-the-line-go-on", true, 0, "lives; echo reached", "reached\n", 0},
		{"a-pipe-command-keeps-its-status", false, -2, "dies; echo reached $?", "reached 130\n", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, _, out := newDrivenSession(t, Config{Instance: "fake", Dir: root, Transport: ExecTransport(exitTransport{code: tt.code}.Exec)})
			if tt.sized {
				s.Resize(80, 24)
			}

			status, err := s.RunLine(t.Context(), tt.line)
			require.NoError(t, err)

			assert.Equal(t, tt.want, out.String())
			assert.Equal(t, tt.status, status)
		})
	}
}
