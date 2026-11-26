// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package tuikit_test

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/integration/tuikit"
)

// app is a small program with one of everything a test drives: keys, text,
// a command that answers later, the window size and quit.
type app struct {
	// release lets the command of "l" answer.
	release chan struct{}

	width, height int
	count         int
	lastKey       string
	text          string
	status        string
	steps         []string
}

type (
	loadedMsg struct{}
	stepMsg   string
)

func step(name string) tea.Cmd {
	return func() tea.Msg { return stepMsg(name) }
}

func newApp() *app {
	return &app{release: make(chan struct{}), status: "idle"}
}

func (a *app) Init() tea.Cmd { return nil }

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
	case loadedMsg:
		a.status = "loaded"
	case stepMsg:
		a.steps = append(a.steps, string(msg))
	case tea.KeyPressMsg:
		a.lastKey = msg.String()
		switch a.lastKey {
		case "+":
			a.count++
		case "ctrl+l":
			a.status = "loading"
			return a, func() tea.Msg {
				<-a.release
				return loadedMsg{}
			}
		case "ctrl+s":
			return a, tea.Sequence(step("one"), step("two"), step("three"))
		case "ctrl+b":
			return a, tea.Batch(step("batched"), step("batched"))
		case "ctrl+x":
			panic("the program is broken")
		case "ctrl+w":
			<-a.release
		case "ctrl+q":
			return a, tea.Quit
		default:
			a.text += msg.Text
		}
	}

	return a, nil
}

func (a *app) View() tea.View {
	v := tea.NewView(strings.Join([]string{
		fmt.Sprintf("size: %dx%d", a.width, a.height),
		fmt.Sprintf("count: %d", a.count),
		fmt.Sprintf("key: %s", a.lastKey),
		fmt.Sprintf("text: %s", a.text),
		fmt.Sprintf("status: %s", a.status),
		fmt.Sprintf("steps: %s", strings.Join(a.steps, " ")),
		strings.Repeat("=", 200),
		"end",
	}, "\n"))
	v.AltScreen = true

	return v
}

// blank is a program that shows nothing.
type blank struct{}

func (blank) Init() tea.Cmd                       { return nil }
func (blank) Update(tea.Msg) (tea.Model, tea.Cmd) { return blank{}, nil }
func (blank) View() tea.View                      { return tea.NewView("") }

// failure runs fn with a testing.TB that records a Fatalf instead of
// failing t, and returns its message, or "" if fn did not fail.
func failure(t *testing.T, fn func(tb testing.TB)) string {
	t.Helper()

	rec := &recorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(rec)
	}()
	<-done

	return rec.msg
}

type recorder struct {
	testing.TB
	msg string
}

func (r *recorder) Helper() {}

func (r *recorder) Fatalf(format string, args ...any) {
	r.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func TestRunner(t *testing.T) {
	t.Parallel()

	t.Run("the screen starts as the view at the default size", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp())

		r.WaitMatch(t, "size: 80x24")
	})

	t.Run("a snapshot right after the start has the first frame", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp())

		assert.Contains(t, r.Snapshot(), "status: idle")
	})

	t.Run("a program that shows nothing starts without a wait", func(t *testing.T) {
		t.Parallel()

		start := time.Now()
		r := tuikit.Start(t, blank{}, tuikit.WithTimeout(10*time.Second))
		assert.Less(t, time.Since(start), 5*time.Second)
		assert.Empty(t, r.Snapshot())
	})

	t.Run("the screen is the size that was asked for", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp(), tuikit.WithSize(30, 7))

		snap := r.WaitMatch(t, "size: 30x7")
		lines := strings.Split(snap, "\n")
		require.Len(t, lines, 7)
		assert.Equal(t, strings.Repeat("=", 30), lines[6], "a long line is cut at the width")
		assert.NotContains(t, snap, "end", "the eighth line of the view is off the screen")
	})

	t.Run("a pressed key reaches the program", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp())

		r.Press(t, "+", "+", "+")
		r.WaitMatch(t, "count: 3")
	})

	t.Run("named keys and modifiers reach the program as themselves", func(t *testing.T) {
		t.Parallel()

		for _, key := range []string{
			"enter", "tab", "esc", "backspace", "space",
			"up", "down", "left", "right",
			"home", "end", "pgup", "pgdown", "delete",
			"shift+tab", "ctrl+a", "alt+x", "?", "G",
			"shift+up", "ctrl+right", "alt+enter", "ctrl+shift+end", "shift+delete",
			"alt+shift+up", "ctrl+alt+left", "alt+pgup",
		} {
			t.Run(key, func(t *testing.T) {
				t.Parallel()
				r := tuikit.Start(t, newApp())

				r.Press(t, key)
				r.WaitMatch(t, "key: "+key+"\n")
			})
		}
	})

	t.Run("a key that cannot be parsed fails the test", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp())

		msg := failure(t, func(tb testing.TB) { r.Press(tb, "ctrl+") })
		assert.Contains(t, msg, `press "ctrl+"`)
	})

	t.Run("a key that a terminal cannot send fails the test", func(t *testing.T) {
		t.Parallel()

		for _, key := range []string{"super+a", "ctrl+shift+a", "shift+enter", "alt+shift+enter", "alt+[", "alt+shift+o", "alt+shift+p"} {
			t.Run(key, func(t *testing.T) {
				t.Parallel()
				r := tuikit.Start(t, newApp())

				msg := failure(t, func(tb testing.TB) { r.Press(tb, key) })
				assert.Contains(t, msg, "a terminal cannot send "+key)
			})
		}
	})

	t.Run("a shifted letter reaches the program as its capital", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp())

		r.Press(t, "shift+g")
		r.WaitMatch(t, "key: G\n")

		r.Press(t, "alt+shift+g")
		r.WaitMatch(t, "key: alt+shift+g\n")
	})

	t.Run("typed text reaches the program in order", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp())

		r.Type(t, "hello, wörld")
		r.WaitMatch(t, "text: hello, wörld")
	})

	t.Run("the screen follows a command that answers later", func(t *testing.T) {
		t.Parallel()
		a := newApp()
		r := tuikit.Start(t, a)

		r.Press(t, "ctrl+l")
		r.WaitMatch(t, "status: loading")

		close(a.release)
		r.WaitNotMatch(t, "status: loading")
		assert.Contains(t, r.Snapshot(), "status: loaded")
	})

	t.Run("Wait returns the text that its condition accepted", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp())

		r.Press(t, "+")
		snap := r.Wait(t, func(text string, exists bool) bool {
			return exists && strings.Contains(text, "count: 1") && strings.Contains(text, "key: +")
		})
		assert.Contains(t, snap, "count: 1")
	})

	t.Run("a condition that never holds fails the test", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp(), tuikit.WithTimeout(50*time.Millisecond))

		msg := failure(t, func(tb testing.TB) { r.Wait(tb, func(string, bool) bool { return false }) })
		assert.Contains(t, msg, "gave up waiting for a condition on the screen after 50ms")
	})

	t.Run("text that never shows fails the test with the last screen", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp(), tuikit.WithTimeout(50*time.Millisecond))

		msg := failure(t, func(tb testing.TB) { r.WaitMatch(tb, "status: loaded") })
		assert.Contains(t, msg, `gave up waiting for the screen to contain "status: loaded" after 50ms`)
		assert.Contains(t, msg, "last screen:\nsize: 80x24")
		assert.Contains(t, msg, "status: idle")
	})

	t.Run("text that never goes fails the test", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp(), tuikit.WithTimeout(50*time.Millisecond))

		msg := failure(t, func(tb testing.TB) { r.WaitNotMatch(tb, "status: idle") })
		assert.Contains(t, msg, `gave up waiting for the screen to not contain "status: idle"`)
	})

	t.Run("the commands of a sequence run in order", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp())

		r.Press(t, "ctrl+s")
		r.WaitMatch(t, "steps: one two three")
	})

	t.Run("the commands of a batch all run", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp())

		r.Press(t, "ctrl+b")
		r.WaitMatch(t, "steps: batched batched")
	})

	t.Run("a wait on a program that has quit fails the test", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp())

		r.Press(t, "ctrl+q")
		msg := failure(t, func(tb testing.TB) { r.WaitMatch(tb, "status: loaded") })
		assert.Contains(t, msg, "the program has ended")
		require.ErrorIs(t, r.Err(), tuikit.ErrStopped)
	})

	t.Run("a wait on a program that has panicked fails the test with the panic", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp())

		r.Press(t, "ctrl+x")
		msg := failure(t, func(tb testing.TB) { r.WaitMatch(tb, "status: loaded") })
		assert.Contains(t, msg, "the program has ended")
		assert.Contains(t, msg, "panic")
	})

	t.Run("a wait for text to go fails on a program that has ended", func(t *testing.T) {
		t.Parallel()

		for _, key := range []string{"ctrl+q", "ctrl+x"} {
			t.Run(key, func(t *testing.T) {
				t.Parallel()
				r := tuikit.Start(t, newApp())

				r.WaitMatch(t, "status: idle")
				r.Press(t, key)
				msg := failure(t, func(tb testing.TB) { r.WaitNotMatch(tb, "status: idle") })
				assert.Contains(t, msg, "the program has ended")
				assert.Contains(t, msg, "last screen:\nsize: 80x24", "the screen from before it ended")

				msg = failure(t, func(tb testing.TB) { r.Line(4).WaitNotMatch(tb, "idle") })
				assert.Contains(t, msg, "the program has ended")
			})
		}
	})

	t.Run("an escape is not the alt of the key after it", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp())

		esc, err := tuikit.ParseKey("esc")
		require.NoError(t, err)
		x, err := tuikit.ParseKey("x")
		require.NoError(t, err)
		require.NoError(t, r.PressKeys(esc, x))
		r.WaitMatch(t, "key: x\ntext: x")
	})

	t.Run("a key that the program does not update for fails the test", func(t *testing.T) {
		t.Parallel()
		a := newApp()
		r := tuikit.Start(t, a, tuikit.WithTimeout(50*time.Millisecond))
		t.Cleanup(func() { close(a.release) })

		msg := failure(t, func(tb testing.TB) { r.Press(tb, "ctrl+w") })
		assert.Contains(t, msg, `press "ctrl+w": tuikit: the program did not update for its input within 50ms`)

		msg = failure(t, func(tb testing.TB) { r.Type(tb, "hello") })
		assert.Contains(t, msg, `type "hello"`)
	})

	t.Run("a program that quits stops taking keys", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp())

		r.Press(t, "+")
		r.WaitMatch(t, "count: 1")
		r.Press(t, "ctrl+q")

		// Keys after the quit are dropped, and do not block the test.
		r.Press(t, "+", "+")
	})

	t.Run("a runner stops while a command is still running", func(t *testing.T) {
		t.Parallel()
		a := newApp()
		r := tuikit.New(a)

		r.Press(t, "ctrl+l")
		r.WaitMatch(t, "status: loading")

		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			r.Stop()
		}()
		close(a.release)

		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Fatal("Stop did not return")
		}
	})
}
