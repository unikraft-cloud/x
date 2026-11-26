// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package tuikit_test

import (
	"image"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/integration/tuikit"
)

// slides is a program that shows one picture at a time, and the next one
// each time a key is pressed.
type slides []string

func (s slides) Init() tea.Cmd { return nil }

func (s slides) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.KeyPressMsg); ok && len(s) > 1 {
		return s[1:], nil
	}
	return s, nil
}

func (s slides) View() tea.View {
	v := tea.NewView(s[0])
	v.AltScreen = true
	return v
}

// show starts a program that shows each of pictures in turn, on a screen of
// 40 by 12, and waits for the first to be drawn. A picture starts after the
// new line that opens it.
func show(t *testing.T, pictures ...string) *tuikit.Runner {
	t.Helper()

	for i, p := range pictures {
		pictures[i] = strings.TrimPrefix(p, "\n")
	}
	r := tuikit.Start(t, slides(pictures), tuikit.WithSize(40, 12))
	r.Wait(t, func(text string, _ bool) bool { return text == strings.TrimRight(ansi.Strip(pictures[0]), "\n") })

	return r
}

const list = `
header
alpha
beta
gamma
footer`

func TestRegion(t *testing.T) {
	t.Parallel()

	t.Run("the runner is the region of the whole screen", func(t *testing.T) {
		t.Parallel()
		r := show(t, list)

		assert.True(t, r.Exists())
		assert.Equal(t, "header\nalpha\nbeta\ngamma\nfooter", r.Snapshot())
		bounds, ok := r.Bounds()
		require.True(t, ok)
		assert.Equal(t, image.Rect(0, 0, 40, 12), bounds)
		assert.Equal(t, "the screen", r.String())
	})

	t.Run("a line is found by its number, from the top or from the bottom", func(t *testing.T) {
		t.Parallel()
		r := show(t, list)

		assert.Equal(t, "header", r.Line(0).Snapshot())
		assert.Equal(t, "beta", r.Line(2).Snapshot())
		// The screen has 12 lines, and the picture only the first 5 of them.
		assert.Empty(t, r.Line(-1).Snapshot())
		assert.Equal(t, "footer", r.Line(-8).Snapshot())

		bounds, ok := r.Line(2).Bounds()
		require.True(t, ok)
		assert.Equal(t, image.Rect(0, 2, 40, 3), bounds)
	})

	t.Run("a line past the end of the region does not exist", func(t *testing.T) {
		t.Parallel()
		r := show(t, list)

		for _, n := range []int{12, -13} {
			assert.False(t, r.Line(n).Exists(), n)
			assert.Empty(t, r.Line(n).Snapshot(), n)
		}
	})

	t.Run("lines are found from one to another, and both are included", func(t *testing.T) {
		t.Parallel()
		r := show(t, list)

		assert.Equal(t, "alpha\nbeta\ngamma", r.Lines(1, 3).Snapshot())
		assert.Equal(t, "gamma\nfooter", r.Lines(3, -8).Snapshot())
		assert.False(t, r.Lines(3, 1).Exists(), "the first is after the last")
		assert.False(t, r.Lines(0, 12).Exists(), "the last is past the end")
	})

	t.Run("a line is found by its text", func(t *testing.T) {
		t.Parallel()
		r := show(t, list)

		assert.Equal(t, "gamma", r.LineMatch("amm").Snapshot())
		assert.Equal(t, "header", r.LineMatch("a").Snapshot(), "the first line of several")
		assert.False(t, r.LineMatch("delta").Exists())
	})

	t.Run("a region is found inside another", func(t *testing.T) {
		t.Parallel()
		r := show(t, list)
		body := r.Lines(1, 3)

		assert.Equal(t, "alpha", body.Line(0).Snapshot())
		assert.Equal(t, "gamma", body.Line(-1).Snapshot())
		assert.False(t, body.LineMatch("footer").Exists(), "the footer is outside of the lines")
		assert.Equal(t, "Line(-1) of Lines(1, 3) of the screen", body.Line(-1).String())
	})

	t.Run("an inset region leaves out its edges", func(t *testing.T) {
		t.Parallel()
		r := show(t, list)

		// The screen is 40 columns, so the last column to go is an empty one.
		assert.Equal(t, "lpha\neta\namma", r.Lines(0, 4).Inset(1).Snapshot())
		assert.False(t, r.Line(0).Inset(1).Exists(), "nothing is left of one line")
		assert.False(t, r.Inset(-1).Exists(), "a region does not grow")
		assert.Empty(t, r.Line(2).Inset(-1).Snapshot())
	})

	t.Run("a wide character takes two columns", func(t *testing.T) {
		t.Parallel()
		r := show(t, `
+------+
|日本語|
|abcdef|
+------+`)

		// The inset cuts the first column of both lines, and the three wide
		// characters are as wide as abcdef.
		assert.Equal(t, "日本語|\nabcdef|", r.Lines(0, 3).Inset(1).Snapshot())
	})

	t.Run("a region follows the screen", func(t *testing.T) {
		t.Parallel()
		r := show(t, "\nstatus: loading\nlogs", "\nlogs\nmore logs\nstatus: ready")
		status := r.LineMatch("status")

		assert.Equal(t, "status: loading", status.Snapshot())

		r.Press(t, "n")
		assert.Equal(t, "status: ready", status.WaitMatch(t, "ready"))
		bounds, ok := status.Bounds()
		require.True(t, ok)
		assert.Equal(t, 2, bounds.Min.Y, "the line moved, and the region with it")
	})

	t.Run("WaitExists waits for a region that is not there yet", func(t *testing.T) {
		t.Parallel()
		r := show(t, "\nloading", "\nloading\nresult: 42")
		result := r.LineMatch("result")

		assert.False(t, result.Exists())

		r.Press(t, "n")
		assert.Equal(t, "result: 42", result.WaitExists(t))
	})

	t.Run("WaitNotMatch waits for the region to exist too", func(t *testing.T) {
		t.Parallel()
		r := show(t, "\nstarting", "\nstatus: loading", "\nstatus: ready")
		status := r.LineMatch("status")

		// There is no status line yet, which is not a status that has loaded.
		r.Press(t, "n", "n")
		assert.Equal(t, "status: ready", status.WaitNotMatch(t, "loading"))
	})

	t.Run("a wait that gives up says which region, and shows the whole screen", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, slides{"header\nstatus: loading"}, tuikit.WithSize(40, 12), tuikit.WithTimeout(50*time.Millisecond))
		status := r.Lines(1, -1).LineMatch("status")

		msg := failure(t, func(tb testing.TB) { status.WaitMatch(tb, "ready") })
		assert.Contains(t, msg, `gave up waiting for LineMatch("status") of Lines(1, -1) of the screen to contain "ready" after 50ms`)
		assert.Contains(t, msg, "last screen:\nheader\nstatus: loading")

		msg = failure(t, func(tb testing.TB) { r.LineMatch("result").WaitExists(tb) })
		assert.Contains(t, msg, `gave up waiting for LineMatch("result") of the screen to exist`)

		msg = failure(t, func(tb testing.TB) { r.LineMatch("result").WaitNotMatch(tb, "loading") })
		assert.Contains(t, msg, `LineMatch("result") of the screen to not contain "loading"`)
	})

	t.Run("a wait for a pattern returns once the region matches it", func(t *testing.T) {
		t.Parallel()
		r := show(t, list, "header\nalpha 12\nbeta")

		r.Press(t, "n")
		assert.Equal(t, "alpha 12", r.Line(1).WaitMatchRegexp(t, `^alpha \d+$`))
	})

	t.Run("a wait for no match of a pattern returns once it is gone", func(t *testing.T) {
		t.Parallel()
		r := show(t, "loading 12%", "done")

		r.Press(t, "n")
		assert.Equal(t, "done", r.Line(0).WaitNotMatchRegexp(t, `\d+%`))

		r = tuikit.Start(t, slides{list}, tuikit.WithTimeout(50*time.Millisecond))
		msg := failure(t, func(tb testing.TB) { r.WaitNotMatchRegexp(tb, `al.ha`) })
		assert.Contains(t, msg, `the screen to not match "al.ha"`)
	})

	t.Run("a pattern that never matches fails the test", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, slides{list}, tuikit.WithTimeout(50*time.Millisecond))

		msg := failure(t, func(tb testing.TB) { r.Line(1).WaitMatchRegexp(tb, `^\d+$`) })
		assert.Contains(t, msg, `gave up waiting for Line(1) of the screen to match "^\\d+$" after 50ms`)

		msg = failure(t, func(tb testing.TB) { r.WaitMatchRegexp(tb, `(`) })
		assert.Contains(t, msg, "error parsing regexp")
	})
}

func TestMatch(t *testing.T) {
	t.Parallel()

	t.Run("text is the cells that show it", func(t *testing.T) {
		t.Parallel()
		r := show(t, list)

		bounds, ok := r.Match("eta").Bounds()
		require.True(t, ok)
		assert.Equal(t, image.Rect(1, 2, 4, 3), bounds)
		assert.Equal(t, "eta", r.Match("eta").Snapshot())
	})

	t.Run("text is the first place that shows it", func(t *testing.T) {
		t.Parallel()
		r := show(t, "one two\ntwo two")

		bounds, ok := r.Match("two").Bounds()
		require.True(t, ok)
		assert.Equal(t, image.Rect(4, 0, 7, 1), bounds)

		bounds, ok = r.Line(1).Match("two").Bounds()
		require.True(t, ok)
		assert.Equal(t, image.Rect(0, 1, 3, 2), bounds)
	})

	t.Run("a wide character takes two columns of its text", func(t *testing.T) {
		t.Parallel()
		r := show(t, "a 日本語 b")

		bounds, ok := r.Match("本語").Bounds()
		require.True(t, ok)
		assert.Equal(t, image.Rect(4, 0, 8, 1), bounds)
	})

	t.Run("text that is not there, or is over two lines, is not found", func(t *testing.T) {
		t.Parallel()
		r := show(t, list)

		assert.False(t, r.Match("delta").Exists())
		assert.False(t, r.Match("alpha\nbeta").Exists())
		assert.False(t, r.Match("").Exists())
		assert.Equal(t, `Match("delta") of Line(1) of the screen`, r.Line(1).Match("delta").String())
	})
}

func TestMatchRegexp(t *testing.T) {
	t.Parallel()

	t.Run("a pattern is the cells that it matches", func(t *testing.T) {
		t.Parallel()
		r := show(t, "name  state\ntest1 up 12s\ntest2 up 3m 日本 x")

		assert.Equal(t, "12s", r.MatchRegexp(`\d+[sm]`).Snapshot())
		bounds, ok := r.Line(2).MatchRegexp(`\d+m \p{Han}+`).Bounds()
		require.True(t, ok)
		assert.Equal(t, image.Rect(9, 2, 16, 3), bounds)
		assert.Equal(t, "test2", r.MatchRegexp(`^test2`).Snapshot())
		assert.Equal(t, "12s", r.MatchRegexp(`\d+s$`).Snapshot(), "a line ends at its last character")
		assert.Equal(t, "name  state", r.LineMatchRegexp(`^name\s+state$`).Snapshot())
		assert.Equal(t, "test1 up 12s", r.LineMatchRegexp(`\d+s`).Snapshot())
	})

	t.Run("a pattern that matches nothing is not found", func(t *testing.T) {
		t.Parallel()
		r := show(t, list)

		assert.False(t, r.MatchRegexp(`\d+`).Exists())
		assert.False(t, r.MatchRegexp(`x*`).Exists(), "a match of no text is not a place")
		assert.False(t, r.MatchRegexp(`alpha\sbeta`).Exists())
		assert.Equal(t, "MatchRegexp(\"\\\\d+\") of the screen", r.MatchRegexp(`\d+`).String())
		assert.Panics(t, func() { r.MatchRegexp(`(`) })
	})
}
