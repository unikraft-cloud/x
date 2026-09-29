// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/stdio"
)

// helperEnv puts the test binary into a mode where it is the shell itself,
// reading the terminal it was given
const helperEnv = "SHELL_PROMPT_HELPER"

func TestMain(m *testing.M) {
	if root := os.Getenv(helperEnv); root != "" {
		os.Exit(runHelperSession(root))
	}
	os.Exit(m.Run())
}

func runHelperSession(root string) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	code, err := Run(ctx, Config{
		Instance:  "fake",
		Dir:       root,
		Transport: local(),
		Builtins:  builtinsNamed("start"),
		Banner:    "this shell is experimental\nno job control, so no ctrl-z, bg or fg",
	}, stdio.Stdio{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr})
	switch {
	case errors.Is(err, context.Canceled):
		// Told to leave, and did: what the CLI treats as a clean exit too.
		return 0
	case err != nil:
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return code
}

func TestPromptNeedsTheProcessTerminal(t *testing.T) {
	ptmx, tty, err := pty.Open()
	require.NoError(t, err)
	t.Cleanup(func() { _ = ptmx.Close(); _ = tty.Close() })

	_, err = Run(t.Context(), Config{
		Instance:  "fake",
		Dir:       newFixture(t),
		Transport: local(),
	}, stdio.Stdio{Stdin: tty, Stdout: tty, Stderr: tty})
	require.ErrorIs(t, err, errNotProcessTerminal)
}

// terminal is the test's end of a session running under a pty.
type terminal struct {
	t    *testing.T
	ptmx *os.File
	cmd  *exec.Cmd

	// drained closes once the recorder has read everything the session wrote.
	drained chan struct{}

	mu        sync.Mutex
	buf       strings.Builder
	last      time.Time // when the session last wrote anything
	mark      int       // how much of buf there was when the test last typed
	forgotten int       // how much of buf the test has chosen not to see

	queries  int // cursor positions readline has asked for, and been told
	queryEnd int // where in buf the last of those questions ended
}

// settled is how long the session has to stay quiet, once a prompt is up,
// before it is taken to be reading
const settled = 150 * time.Millisecond

// prompted is how the screen ends once a prompt is up
var prompted = regexp.MustCompile(`(\$ |> )(\r\r\n)?$`)

func newTerminal(t *testing.T, root string) *terminal {
	t.Helper()

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), helperEnv+"="+root)

	ptmx, err := pty.Start(cmd)
	require.NoError(t, err)
	require.NoError(t, pty.Setsize(ptmx, &pty.Winsize{Rows: 24, Cols: 120}))

	term := &terminal{t: t, ptmx: ptmx, cmd: cmd, drained: make(chan struct{})}
	go func() {
		defer close(term.drained)
		buf := make([]byte, 4096)
		var tail string
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				term.mu.Lock()
				term.buf.Write(buf[:n])
				term.last = time.Now()
				term.mu.Unlock()
				// A query can straddle two reads; look at the last few bytes as one.
				tail = (tail + string(buf[:n]))
				if len(tail) > 64 {
					tail = tail[len(tail)-64:]
				}
				tail = term.answer(tail)
			}
			if err != nil {
				return
			}
		}
	}()

	t.Cleanup(func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	term.ready()
	return term
}

// ready waits until the session is reading a fresh line
func (term *terminal) ready() {
	term.t.Helper()

	require.Eventuallyf(term.t, func() bool {
		return term.measured() && term.promptedSince() && term.quiet()
	}, 10*time.Second, 20*time.Millisecond, "the prompt never settled in:\n%q", term.seen())
}

// measured is whether readline has asked where the cursor is, been told, and
// drawn since, as a key typed before the answer is taken is lost
func (term *terminal) measured() bool {
	term.mu.Lock()
	defer term.mu.Unlock()
	return term.queries > 0 && term.buf.Len() > term.queryEnd
}

// promptedSince is whether the screen ends in a prompt drawn after the test
// last typed
func (term *terminal) promptedSince() bool {
	since := term.seenSince(term.markAt())
	return prompted.MatchString(since) || strings.HasSuffix(since, "\r\r\n")
}

func (term *terminal) markAt() int {
	term.mu.Lock()
	defer term.mu.Unlock()
	return term.mark
}

// seenSince is what the session has shown since buf was from bytes long, the
// offset being into the raw stream, not the stripped screen
func (term *terminal) seenSince(from int) string {
	term.mu.Lock()
	defer term.mu.Unlock()
	return ansi.Strip(term.buf.String()[from:])
}

// quiet is whether the session has shown nothing new for a while.
func (term *terminal) quiet() bool {
	term.mu.Lock()
	defer term.mu.Unlock()
	return time.Since(term.last) > settled
}

// run types a command line once the prompt is ready for it, in one write so
// that readline takes every key into its own buffer at once
func (term *terminal) run(line string) {
	term.t.Helper()

	term.ready()
	term.send(line + "\r")
	if !acceptMultiline([]rune(line)) {
		// An incomplete line is taken without a redraw; a step to the right draws it
		term.send("\x1b[C")
	}
}

// answer replies to what a terminal is asked, and returns what is left of out
// once the answered queries are cut out
func (term *terminal) answer(out string) string {
	for query, reply := range map[string]string{
		"\x1b]11;?": "\x1b]11;rgb:0000/0000/0000\a",
		"\x1b[c":    "\x1b[?1;2c",
		"\x1b[6n":   "\x1b[1;1R",
	} {
		for {
			at := strings.Index(out, query)
			if at < 0 {
				break
			}
			if query == "\x1b[6n" {
				// What follows the query in out is the tail of buf
				term.mu.Lock()
				term.queries++
				term.queryEnd = term.buf.Len() - (len(out) - at - len(query))
				term.mu.Unlock()
			}
			_, _ = term.ptmx.WriteString(reply)
			out = out[:at] + out[at+len(query):]
		}
	}
	return out
}

func (term *terminal) seen() string {
	term.mu.Lock()
	defer term.mu.Unlock()
	return ansi.Strip(term.buf.String()[term.forgotten:])
}

// forget hides what has been seen so far from seen, and nothing else
func (term *terminal) forget() {
	term.mu.Lock()
	defer term.mu.Unlock()
	term.forgotten = term.buf.Len()
}

func (term *terminal) send(keys string) {
	term.t.Helper()

	term.mu.Lock()
	term.mark = term.buf.Len()
	term.mu.Unlock()

	_, err := term.ptmx.WriteString(keys)
	require.NoError(term.t, err)
}

func (term *terminal) await(want string, why ...string) {
	term.t.Helper()

	require.Eventuallyf(term.t, func() bool {
		return strings.Contains(term.seen(), want)
	}, 10*time.Second, 20*time.Millisecond, "never saw %q (%s) in:\n%s", want, strings.Join(why, " "), term.seen())
}

// awaitMatch waits for what the session has shown so far to match pattern.
func (term *terminal) awaitMatch(pattern string, why ...string) {
	term.t.Helper()

	re := regexp.MustCompile(pattern)
	require.Eventuallyf(term.t, func() bool {
		return re.MatchString(term.seen())
	}, 10*time.Second, 20*time.Millisecond, "never matched %q (%s) in %q", pattern, strings.Join(why, " "), term.seen())
}

// wait lets the session finish, so that what it printed last is in hand.
func (term *terminal) wait() {
	term.t.Helper()

	_ = term.cmd.Wait()
	<-term.drained
}

// left is whether the session exits by itself within a few seconds.
func (term *terminal) left() bool {
	term.t.Helper()

	gone := make(chan struct{})
	go func() { term.wait(); close(gone) }()
	select {
	case <-gone:
		return true
	case <-time.After(5 * time.Second):
		return false
	}
}

func TestPrompt(t *testing.T) {
	root := newFixture(t)

	t.Run("says-what-it-cannot-do", func(t *testing.T) {
		term := newTerminal(t, root)

		term.await("experimental")
		term.await("no job control")
	})

	t.Run("runs-a-line-on-the-instance", func(t *testing.T) {
		term := newTerminal(t, root)

		term.run("echo hello")
		term.await("hello")
	})

	t.Run("runs-a-command-on-the-instance-and-gets-the-prompt-back", func(t *testing.T) {
		term := newTerminal(t, root)

		term.run("/bin/echo remote-ok")
		term.await("remote-ok")
		term.run("echo after")
		term.await("after", "echo is the interpreter's; /bin/echo went through the transport, which must hand the terminal back")
	})

	t.Run("takes-accents-as-typed", func(t *testing.T) {
		term := newTerminal(t, root)

		term.run("echo h\u00e9llo")
		term.await("h\u00e9llo\r\n", "an accent is a letter, not a meta key")
	})

	t.Run("refuses-a-background-job", func(t *testing.T) {
		term := newTerminal(t, root)

		term.run("echo bg &")
		term.await("no job control")
		term.run("echo after")
		term.await("after")
	})

	t.Run("does-not-exit-on-error", func(t *testing.T) {
		term := newTerminal(t, root)

		term.run("set -e")
		term.run("false")
		term.run("echo alive")
		term.await("alive\r\n", "a failing command under set -e is not the end of the session")
	})

	t.Run("survives-an-unset-parameter", func(t *testing.T) {
		term := newTerminal(t, root)

		term.run("echo ${nope:?is not set}")
		term.await("nope: is not set")
		term.run("echo alive")
		term.await("alive\r\n", "what would end a script ends the statement here")
	})

	t.Run("keeps-the-exit-trap-for-the-exit", func(t *testing.T) {
		term := newTerminal(t, root)

		term.run("trap 'echo trapped' EXIT")
		term.run("echo started; sleep 30")
		term.await("started\r\n")
		term.send("\x03") // ctrl-c
		term.run("echo after")
		term.await("after\r\n")
		assert.Zero(t, strings.Count(term.seen(), "trapped\r\n"), "an interrupt is not an exit")

		term.ready()
		term.send("\x04") // ctrl-d
		require.True(t, term.left(), "^D at the prompt is the exit")
		assert.Equal(t, 1, strings.Count(term.seen(), "trapped\r\n"), "the exit is")
	})

	t.Run("lists-and-recalls-its-history", func(t *testing.T) {
		term := newTerminal(t, root)

		term.run("echo one")
		term.await("one\r\n")
		term.run(":history")
		term.await("1  echo one")
	})

	t.Run("exits-with-the-status-asked-for", func(t *testing.T) {
		term := newTerminal(t, root)

		term.run("exit 3")
		require.True(t, term.left())
		assert.Equal(t, 3, term.cmd.ProcessState.ExitCode())
	})

	t.Run("keeps-the-working-directory", func(t *testing.T) {
		term := newTerminal(t, root)

		term.run("cd var/log")
		term.await("/var/log$ ")
		term.run("pwd")
		term.await(root + "/var/log")
	})

	t.Run("recalls-the-previous-line", func(t *testing.T) {
		term := newTerminal(t, root)

		term.run("echo alpha")
		term.await("alpha")

		term.ready()
		term.forget()
		term.send("\x1b[A") // up
		term.await("echo alpha")
		term.send("\r")
		term.await("alpha")
	})

	t.Run("searches-what-it-has-run", func(t *testing.T) {
		term := newTerminal(t, root)

		term.run("echo needle")
		term.await("needle")
		term.ready()
		term.send("\x12needle") // ctrl-r
		term.await("echo needle")
		term.send("\r")
		term.await("needle")
	})

	t.Run("keeps-reading-an-incomplete-line", func(t *testing.T) {
		term := newTerminal(t, root)

		term.run("for i in 1 2 3; do")
		term.run("printf %s $i")
		term.run("done")
		term.await("123")
	})

	t.Run("interrupts-the-command-and-keeps-the-session", func(t *testing.T) {
		term := newTerminal(t, root)

		term.run("echo started; sleep 30")
		term.await("started\r\n")
		term.send("\x03") // ctrl-c
		term.awaitMatch(`\^C\r?\n[^\n]*\$ `, "the prompt starts on a fresh line after the ^C")

		term.run("echo after")
		term.await("after")
	})

	t.Run("prints-one-ctrl-c-at-the-prompt", func(t *testing.T) {
		term := newTerminal(t, root)

		term.ready()
		term.send("half a line\x03")
		term.awaitMatch(`\^C\r?\n[^\n]*\$ `, "one ^C, then a fresh prompt")
		assert.NotContains(t, term.seen(), "^C^C", "readline echoed its own ^C next to the session's")
	})

	t.Run("leaves-when-told-to", func(t *testing.T) {
		term := newTerminal(t, root)

		require.NoError(t, term.cmd.Process.Signal(syscall.SIGTERM))
		require.True(t, term.left(), "the session stayed at the prompt after SIGTERM")
		assert.Zero(t, term.cmd.ProcessState.ExitCode(), "told to leave is not a failure")
	})

	t.Run("leaves-when-told-to-mid-search", func(t *testing.T) {
		term := newTerminal(t, root)

		term.ready()
		term.send("\x12needle") // ctrl-r
		term.await("needle")
		require.NoError(t, term.cmd.Process.Signal(syscall.SIGTERM))
		require.True(t, term.left(), "a search in progress kept the session alive")
	})

	t.Run("leaves-when-told-to-mid-command", func(t *testing.T) {
		term := newTerminal(t, root)

		term.run("echo started; sleep 30; echo x")
		term.await("started\r\n")
		require.NoError(t, term.cmd.Process.Signal(syscall.SIGTERM))
		require.True(t, term.left())
		assert.Zero(t, term.cmd.ProcessState.ExitCode())
		assert.NotContains(t, term.seen(), "context canceled", "an orderly exit has nothing to report")
	})

	t.Run("leaves-on-ctrl-d", func(t *testing.T) {
		term := newTerminal(t, root)

		term.run("echo bye")
		term.await("bye")
		term.ready()
		term.send("\x04") // ctrl-d

		require.True(t, term.left(), "^D at the prompt is the exit")
		assert.Zero(t, term.cmd.ProcessState.ExitCode(), "a clean exit")
	})
}
