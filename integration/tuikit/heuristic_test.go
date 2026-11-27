// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package tuikit_test

import (
	"image"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/integration/tuikit"
)

const panels = `
 tests > test1
╭────────────────╮╭─────────────╮
│ Details        ││ Logs        │
│ name  test1    ││ started     │
│ state running  ││ listening   │
╰────────────────╯╰─────────────╯
 esc back`

const stacked = `
╭────────────╮
│ Details    │
│ logs: 2    │
╰────────────╯
╭────────────╮
│ Logs       │
│ started    │
╰────────────╯`

const nested = `
┌────────────────────┐
│ outer              │
│ ┌────────┐         │
│ │ inner  │         │
│ └────────┘         │
└────────────────────┘`

const captions = `
╭─ Logs ─────────╮
│ started        │
╰────────────────╯
╭────────────────╮
│ started        │
╰──────── 3/40 ──╯`

const table = `
┌───────┬───────┐
│ name  │ state │
├───────┼───────┤
│ test1 │ up    │
└───────┴───────┘`

func TestBox(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		picture string
		box     tuikit.Box
		// want is the box that is found, or empty for none.
		want string
	}{
		{
			name:    "a box is found by text inside of it, with its border",
			picture: panels,
			box:     tuikit.Box{Match: "listening"},
			want: `
╭─────────────╮
│ Logs        │
│ started     │
│ listening   │
╰─────────────╯`,
		},
		{
			name:    "text that no box has finds nothing",
			picture: panels,
			box:     tuikit.Box{Match: "esc back"},
		},
		{
			name:    "a box with no text asked of it is the smallest one",
			picture: panels,
			box:     tuikit.Box{},
			want: `
╭─────────────╮
│ Logs        │
│ started     │
│ listening   │
╰─────────────╯`,
		},
		{
			name:    "boxes that touch are not one box",
			picture: stacked,
			box:     tuikit.Box{Match: "Details"},
			want: `
╭────────────╮
│ Details    │
│ logs: 2    │
╰────────────╯`,
		},
		{
			name:    "the first line is the one under the top border",
			picture: stacked,
			box:     tuikit.Box{Match: "ogs", Line: new(0)},
			want: `
╭────────────╮
│ Logs       │
│ started    │
╰────────────╯`,
		},
		{
			name:    "the last line is the one above the bottom border",
			picture: stacked,
			box:     tuikit.Box{Match: "ogs", Line: new(-1)},
			want: `
╭────────────╮
│ Details    │
│ logs: 2    │
╰────────────╯`,
		},
		{
			name:    "a line that the box does not have finds nothing",
			picture: stacked,
			box:     tuikit.Box{Line: new(2)},
		},
		{
			name:    "the smallest box with the text is found",
			picture: nested,
			box:     tuikit.Box{Match: "inner"},
			want: `
┌────────┐
│ inner  │
└────────┘`,
		},
		{
			name:    "a box is found around another",
			picture: nested,
			box:     tuikit.Box{Match: "outer"},
			want: `
┌────────────────────┐
│ outer              │
│ ┌────────┐         │
│ │ inner  │         │
│ └────────┘         │
└────────────────────┘`,
		},
		{
			name:    "a label in the top border finds its box",
			picture: captions,
			box:     tuikit.Box{Label: "Logs"},
			want: `
╭─ Logs ─────────╮
│ started        │
╰────────────────╯`,
		},
		{
			name:    "a label in the bottom border finds its box",
			picture: captions,
			box:     tuikit.Box{Label: "3/40"},
			want: `
╭────────────────╮
│ started        │
╰──────── 3/40 ──╯`,
		},
		{
			name:    "a label and text inside have to be in the same box",
			picture: captions,
			box:     tuikit.Box{Label: "Logs", Match: "started", Line: new(0)},
			want: `
╭─ Logs ─────────╮
│ started        │
╰────────────────╯`,
		},
		{
			name:    "a border with a caption is not a border without its label",
			picture: captions,
			box:     tuikit.Box{Match: "started"},
		},
		{
			name:    "a label that no border has finds nothing",
			picture: captions,
			box:     tuikit.Box{Label: "Details"},
		},
		{
			name:    "text inside a box is not its label",
			picture: captions,
			box:     tuikit.Box{Label: "started"},
		},
		{
			name: "a border that is broken twice has no label",
			picture: `
╭─ A ── Logs ────╮
│ started        │
╰────────────────╯`,
			box: tuikit.Box{Label: "Logs"},
		},
		{
			name: "a label can have a border character in it",
			picture: `
+-- up-to-date --+
| started        |
+----------------+`,
			box: tuikit.Box{Label: "up-to-date"},
			want: `
+-- up-to-date --+
| started        |
+----------------+`,
		},
		{
			name:    "a cell of a table is a box",
			picture: table,
			box:     tuikit.Box{Match: "state"},
			want: `
┬───────┐
│ state │
┼───────┤`,
		},
		{
			name: "a rule inside a box does not split it",
			picture: `
╭────────╮
│ title  │
│────────│
│ body   │
╰────────╯`,
			box: tuikit.Box{Match: "body"},
			want: `
╭────────╮
│ title  │
│────────│
│ body   │
╰────────╯`,
		},
		{
			name: "an ASCII box is found",
			picture: `
+--------+
| hello  |
+--------+`,
			box: tuikit.Box{Match: "hello"},
			want: `
+--------+
| hello  |
+--------+`,
		},
		{
			name: "a border that is broken is not a box",
			picture: `
+---  ---+
| hello  |
+--------+`,
			box: tuikit.Box{Match: "hello"},
		},
		{
			name: "a side that is broken is not a box",
			picture: `
+--------+
| hello
+--------+`,
			box: tuikit.Box{Match: "hello"},
		},
		{
			name: "bars around text are not a box",
			picture: `
| hello  |
| world  |
| again  |`,
			box: tuikit.Box{},
		},
		{
			name: "other characters are a border when they are asked for",
			picture: `
##########
# hello  #
##########`,
			box: tuikit.Box{Match: "hello", Chars: "#"},
			want: `
##########
# hello  #
##########`,
		},
		{
			name: "other characters are not a border by default",
			picture: `
##########
# hello  #
##########`,
			box: tuikit.Box{Match: "hello"},
		},
		{
			name: "the default characters are not a border when others are asked for",
			picture: `
+--------+
| hello  |
+--------+`,
			box: tuikit.Box{Match: "hello", Chars: "#"},
		},
		{
			name: "a wide character inside takes two columns of the border",
			picture: `
╭────────╮
│ 日本語 │
╰────────╯`,
			box: tuikit.Box{Match: "日本語"},
			want: `
╭────────╮
│ 日本語 │
╰────────╯`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := show(t, tc.picture)
			box := r.Heuristic(tc.box)

			if tc.want == "" {
				assert.False(t, box.Exists(), "found:\n%s", box.Snapshot())
				return
			}
			require.True(t, box.Exists())
			assert.Equal(t, strings.TrimPrefix(tc.want, "\n"), box.Snapshot())
		})
	}
}

func TestBoxRegion(t *testing.T) {
	t.Parallel()

	t.Run("a box is where it is drawn", func(t *testing.T) {
		t.Parallel()
		r := show(t, panels)

		bounds, ok := r.Heuristic(tuikit.Box{Match: "Logs"}).Bounds()
		require.True(t, ok)
		assert.Equal(t, image.Rect(18, 1, 33, 6), bounds)
	})

	t.Run("the inside of a box is its inset", func(t *testing.T) {
		t.Parallel()
		r := show(t, panels)
		logs := r.Heuristic(tuikit.Box{Match: "Logs", Line: new(0)})

		assert.Equal(t, " Logs\n started\n listening", logs.Inset(1).Snapshot())
		assert.Equal(t, " listening", logs.Inset(1).Line(-1).Snapshot())
	})

	t.Run("a box is only looked for inside its region", func(t *testing.T) {
		t.Parallel()
		r := show(t, stacked)

		assert.Equal(t, "│ Logs       │", r.Lines(4, 7).Heuristic(tuikit.Box{}).Line(1).Snapshot())
		assert.False(t, r.Lines(1, 7).Heuristic(tuikit.Box{Match: "Details"}).Exists(), "the top border is outside of the lines")
	})

	t.Run("a box follows the screen", func(t *testing.T) {
		t.Parallel()
		r := show(t, `
loading`, `
╭───────────╮
│ Logs      │
│ started   │
╰───────────╯`, `
status: ready
╭──────────────╮
│ Logs         │
│ started      │
│ listening    │
╰──────────────╯`)
		logs := r.Heuristic(tuikit.Box{Match: "Logs", Line: new(0)})

		assert.False(t, logs.Exists())

		r.Press(t, "n")
		logs.WaitMatch(t, "started")

		r.Press(t, "n")
		logs.WaitMatch(t, "listening")
		bounds, ok := logs.Bounds()
		require.True(t, ok)
		assert.Equal(t, image.Rect(0, 1, 16, 6), bounds)
	})

	t.Run("a wait on a box names it", func(t *testing.T) {
		t.Parallel()
		r := show(t, panels)

		assert.Equal(t,
			`Heuristic(Box{Match: "Logs", Line: 0, Label: "x", Chars: "#"}) of the screen`,
			r.Heuristic(tuikit.Box{Match: "Logs", Line: new(0), Label: "x", Chars: "#"}).String())
		assert.Equal(t, "Heuristic(Box{}) of the screen", r.Heuristic(tuikit.Box{}).String())
	})
}
