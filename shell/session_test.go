// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/stdio"
)

// newDrivenSession is a session a caller drives, with the write end of the
// keystroke pipe to type into.
func newDrivenSession(t *testing.T, cfg Config) (*Session, *os.File, *captured) {
	t.Helper()

	pr, pw, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = pw.Close()
		_ = pr.Close()
	})

	out := &captured{}
	cfg.Input = pr
	if cfg.Transport == nil {
		cfg.Transport = local()
	}

	s, err := New(t.Context(), cfg, stdio.Stdio{Stdout: out, Stderr: out})
	require.NoError(t, err)
	return s, pw, out
}

func TestSessionRunsLineByLine(t *testing.T) {
	root := newFixture(t)
	s, _, out := newDrivenSession(t, Config{Instance: "fake", Dir: root})

	for _, tt := range []struct {
		line   string
		status int
		want   string
	}{
		{"cd var/log", 0, ""},
		{"pwd", 0, filepath.Join(root, "var", "log") + "\n"},
		{"echo *.log", 0, "app.log boot.log\n"},
		{"x=5", 0, ""},
		{"echo $((x * 2))", 0, "10\n"},
		{"false", 1, ""},
		{"echo $?", 0, "1\n"},
	} {
		t.Run(tt.line, func(t *testing.T) {
			out.Reset()

			status, err := s.RunLine(t.Context(), tt.line)
			require.NoError(t, err)
			assert.Equal(t, tt.status, status)
			assert.Equal(t, tt.want, out.String())
			assert.False(t, s.Exited())
		})
	}

	assert.Equal(t, filepath.Join(root, "var", "log"), s.Dir(), "the session is where it was left")

	status, err := s.RunLine(t.Context(), "exit 3")
	require.NoError(t, err)
	assert.Equal(t, 3, status)
	assert.True(t, s.Exited(), "the session is over")
}

func TestSessionTellsARefusedLineFromAFailedOne(t *testing.T) {
	s, _, out := newDrivenSession(t, Config{Instance: "fake", Dir: "/"})

	status, err := s.RunLine(t.Context(), "sleep 1 &")
	require.Error(t, err, "the shell will not run it at all")
	assert.Equal(t, statusNotRun, status)
	assert.Contains(t, out.String(), "no job control")

	out.Reset()
	status, err = s.RunLine(t.Context(), "echo unclosed '")
	require.Error(t, err)
	assert.Equal(t, statusNotRun, status)
	assert.Contains(t, out.String(), "reached EOF without closing quote",
		"the line is told on the session's own stderr")
}

func TestSessionInterruptEndsTheLine(t *testing.T) {
	transport := newHaltingTransport()
	s, _, out := newDrivenSession(t, Config{Instance: "fake", Dir: "/", Transport: ExecTransport(transport.Exec)})

	go func() {
		<-transport.started
		s.Interrupt()
	}()

	status, err := s.RunLine(t.Context(), blockingCommand+"; echo unreachable")
	require.NoError(t, err)
	assert.Equal(t, StatusInterrupted, status)
	assert.Equal(t, "\n", out.String(), "the rest of the line never ran")

	out.Reset()
	status, err = s.RunLine(t.Context(), "echo next")
	require.NoError(t, err)
	assert.Equal(t, 0, status)
	assert.Equal(t, "next\n", out.String(), "the session goes on")
}

func TestSessionLendsTheKeystrokesToOneCommand(t *testing.T) {
	root := newFixture(t)
	s, keys, out := newDrivenSession(t, Config{Instance: "fake", Dir: root})

	// A command with no redirection of its own reads what is typed.
	_, err := keys.WriteString("payload\n")
	require.NoError(t, err)
	_, err = s.RunLine(t.Context(), "head -1")
	require.NoError(t, err)
	assert.Equal(t, "payload\n", out.String())

	// So does the interpreter's own read, which never reaches a handler.
	out.Reset()
	_, err = keys.WriteString("typed\n")
	require.NoError(t, err)
	_, err = s.RunLine(t.Context(), "read answer; echo got=$answer")
	require.NoError(t, err)
	assert.Equal(t, "got=typed\n", out.String())

	// A redirected command reads the file, and what was typed stays for the next
	// one, as it would on a terminal.
	out.Reset()
	_, err = keys.WriteString("still here\n")
	require.NoError(t, err)
	_, err = s.RunLine(t.Context(), "cat < "+filepath.Join(root, "hostname"))
	require.NoError(t, err)
	assert.Equal(t, "fakebox\n", out.String())

	out.Reset()
	_, err = s.RunLine(t.Context(), "head -1")
	require.NoError(t, err)
	assert.Equal(t, "still here\n", out.String())
}

func TestAReadSurvivesACommandBeforeIt(t *testing.T) {
	s, keys, _ := newDrivenSession(t, Config{Instance: "fake", Dir: "/"})

	// The command takes the keys on loan, which is what a lend that asks for the
	// descriptor would have left unable to take a deadline.
	_, err := keys.WriteString("primed\n")
	require.NoError(t, err)
	_, err = s.RunLine(t.Context(), "head -1")
	require.NoError(t, err)

	go func() {
		time.Sleep(200 * time.Millisecond)
		s.Interrupt()
	}()

	done := make(chan int, 1)
	go func() {
		status, _ := s.RunLine(t.Context(), "read answer")
		done <- status
	}()

	select {
	case status := <-done:
		assert.Equal(t, StatusInterrupted, status)
	case <-time.After(10 * time.Second):
		t.Fatal("^C never got the session back from a read with nothing to read")
	}
}

func TestTheKeystrokesAreLentAgainToTheNextCommand(t *testing.T) {
	s, keys, out := newDrivenSession(t, Config{Instance: "fake", Dir: "/"})

	for _, typed := range []string{"first\n", "second\n"} {
		out.Reset()

		_, err := keys.WriteString(typed)
		require.NoError(t, err)
		_, err = s.RunLine(t.Context(), "head -1")
		require.NoError(t, err)

		// The reader the last command was lent is gone, not parked on the pipe.
		assert.Equal(t, typed, out.String())
	}
}

func TestSessionKeepsItsOwnHistory(t *testing.T) {
	s, _, out := newDrivenSession(t, Config{Instance: "fake", Dir: "/"})

	for _, line := range []string{"echo one", "echo one", "  "} {
		_, err := s.RunLine(t.Context(), line)
		require.NoError(t, err)
	}

	out.Reset()
	_, err := s.RunLine(t.Context(), "history")
	require.NoError(t, err)
	assert.Equal(t, "    1  echo one\n    2  history\n", out.String(),
		"a line is recorded before it runs, blanks and repeats aside")
}

func TestSessionAnswersBareHelp(t *testing.T) {
	s, _, out := newDrivenSession(t, Config{Instance: "fake", Dir: "/"})

	_, err := s.RunLine(t.Context(), "help")
	require.NoError(t, err)
	assert.Contains(t, out.String(), "  :history", "help is the session's, not the interpreter's")
}

func TestSessionPaintsWhatTheCallerAsksFor(t *testing.T) {
	s, _, _ := newDrivenSession(t, Config{Instance: "fake", Dir: "/var"})

	assert.Contains(t, s.Prompt(false), "fake", "the instance names the prompt")
	assert.Contains(t, s.Prompt(false), "/var", "and so does where it is")
	assert.Contains(t, s.Prompt(true), ">")
	assert.Equal(t, "echo hi", ansi.Strip(s.Highlight("echo hi")), "the line survives being painted")

	assert.True(t, AcceptMultiline([]rune("echo hi")))
	assert.False(t, AcceptMultiline([]rune("for i in 1 2; do")))
}

func TestASessionIsNotACommandLine(t *testing.T) {
	_, err := New(t.Context(), Config{Transport: local(), Command: "echo hi"}, stdio.Stdio{})
	require.ErrorContains(t, err, "not cfg.Command")

	_, err = New(t.Context(), Config{Transport: local()},
		stdio.Stdio{Stdin: strings.NewReader("")})
	require.ErrorContains(t, err, "not streams.Stdin")

	_, err = New(t.Context(), Config{}, stdio.Stdio{})
	require.ErrorContains(t, err, "no transport")
}

func TestASessionEndsOnItsTrap(t *testing.T) {
	s, _, out := newDrivenSession(t, Config{Instance: "fake", Dir: "/"})

	_, err := s.RunLine(t.Context(), `trap 'echo goodbye' EXIT`)
	require.NoError(t, err)

	out.Reset()
	require.NoError(t, s.Close(t.Context()))

	assert.Eventually(t, func() bool { return out.String() == "goodbye\n" },
		2*time.Second, 10*time.Millisecond)
}
