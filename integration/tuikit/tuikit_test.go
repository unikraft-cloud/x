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
}

type loadedMsg struct{}

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
		case "ctrl+q":
			return a, tea.Quit
		default:
			a.text += msg.Text
		}
	}

	return a, nil
}

func (a *app) View() tea.View {
	return tea.NewView(strings.Join([]string{
		fmt.Sprintf("size: %dx%d", a.width, a.height),
		fmt.Sprintf("count: %d", a.count),
		fmt.Sprintf("key: %s", a.lastKey),
		fmt.Sprintf("text: %s", a.text),
		fmt.Sprintf("status: %s", a.status),
		strings.Repeat("=", 200),
		"end",
	}, "\n"))
}

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

		r.WaitFor(t, "size: 80x24")
	})

	t.Run("the screen is the size that was asked for", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp(), tuikit.WithSize(30, 6))

		snap := r.WaitFor(t, "size: 30x6")
		lines := strings.Split(snap, "\n")
		require.Len(t, lines, 6)
		assert.Equal(t, strings.Repeat("=", 30), lines[5], "a long line is cut at the width")
		assert.NotContains(t, snap, "end", "the seventh line of the view is off the screen")
	})

	t.Run("a pressed key reaches the program", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp())

		r.Press(t, "+", "+", "+")
		r.WaitFor(t, "count: 3")
	})

	t.Run("named keys and modifiers reach the program as themselves", func(t *testing.T) {
		t.Parallel()

		for _, key := range []string{
			"enter", "tab", "esc", "backspace", "space",
			"up", "down", "left", "right",
			"home", "end", "pgup", "pgdown", "delete",
			"shift+tab", "ctrl+a", "alt+x", "?", "G",
		} {
			t.Run(key, func(t *testing.T) {
				t.Parallel()
				r := tuikit.Start(t, newApp())

				r.Press(t, key)
				r.WaitFor(t, "key: "+key+"\n")
			})
		}
	})

	t.Run("a key that cannot be parsed fails the test", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp())

		msg := failure(t, func(tb testing.TB) { r.Press(tb, "ctrl+") })
		assert.Contains(t, msg, `press "ctrl+"`)
	})

	t.Run("typed text reaches the program in order", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp())

		r.Type(t, "hello, wörld")
		r.WaitFor(t, "text: hello, wörld")
	})

	t.Run("the screen follows a command that answers later", func(t *testing.T) {
		t.Parallel()
		a := newApp()
		r := tuikit.Start(t, a)

		r.Press(t, "ctrl+l")
		r.WaitFor(t, "status: loading")

		close(a.release)
		r.WaitForGone(t, "status: loading")
		assert.Contains(t, r.Snapshot(), "status: loaded")
	})

	t.Run("WaitForFunc returns the snapshot that it accepted", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp())

		r.Press(t, "+")
		snap := r.WaitForFunc(t, func(s string) bool {
			return strings.Contains(s, "count: 1") && strings.Contains(s, "key: +")
		})
		assert.Contains(t, snap, "count: 1")
	})

	t.Run("text that never shows fails the test with the last snapshot", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp(), tuikit.WithTimeout(50*time.Millisecond))

		msg := failure(t, func(tb testing.TB) { r.WaitFor(tb, "status: loaded") })
		assert.Contains(t, msg, `waiting for "status: loaded" after 50ms`)
		assert.Contains(t, msg, "last snapshot:\nsize: 80x24")
		assert.Contains(t, msg, "status: idle")
	})

	t.Run("text that never goes fails the test", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp(), tuikit.WithTimeout(50*time.Millisecond))

		msg := failure(t, func(tb testing.TB) { r.WaitForGone(tb, "status: idle") })
		assert.Contains(t, msg, `waiting for "status: idle" to be gone`)
	})

	t.Run("a program that quits stops taking keys", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, newApp())

		r.Press(t, "+")
		r.WaitFor(t, "count: 1")
		r.Press(t, "ctrl+q")

		// Keys after the quit are dropped, and do not block the test.
		r.Press(t, "+", "+")
	})

	t.Run("a runner stops while a command is still running", func(t *testing.T) {
		t.Parallel()
		a := newApp()
		r := tuikit.New(a)

		r.Press(t, "ctrl+l")
		r.WaitFor(t, "status: loading")

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
