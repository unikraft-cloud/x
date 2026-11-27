// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package tuikit_test

import (
	"image/color"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"

	"unikraft.com/x/integration/tuikit"
)

var (
	blue = ansi.HexColor("#2b7fff")
	grey = ansi.HexColor("#314158")

	selected = ansi.NewStyle().Bold().ForegroundColor(blue).BackgroundColor(grey)
)

func menu(choice string) string {
	lines := "menu\n"
	for _, item := range []string{"alpha", "beta", "日本語"} {
		if item == choice {
			lines += "> " + selected.Styled(item) + " <\n"
		} else {
			lines += "  " + item + "\n"
		}
	}
	return lines + ansi.NewStyle().Faint().Italic(true).Underline(true).Reverse(true).Strikethrough(true).Styled("all")
}

func TestWaitStyle(t *testing.T) {
	t.Parallel()

	t.Run("a style is found on the text that has it", func(t *testing.T) {
		t.Parallel()
		r := show(t, menu("beta"))

		r.Match("beta").WaitStyle(t, tuikit.Style{Bold: new(true)})
		r.Match("beta").WaitStyle(t, tuikit.Style{Foreground: blue, Background: grey})
		r.Match("beta").WaitStyle(t, tuikit.Style{Foreground: color.RGBA{R: 0x2b, G: 0x7f, B: 0xff, A: 0xff}})
		r.Match("alpha").WaitStyle(t, tuikit.Style{Bold: new(false), Reverse: new(false)})
		r.Match("all").WaitStyle(t, tuikit.Style{
			Faint: new(true), Italic: new(true), Underline: new(true), Reverse: new(true), Strikethrough: new(true),
		})
		r.WaitStyle(t, tuikit.Style{})
	})

	t.Run("a style follows the screen", func(t *testing.T) {
		t.Parallel()
		r := show(t, menu("alpha"), menu("日本語"))

		r.Match("alpha").WaitStyle(t, tuikit.Style{Bold: new(true)})
		r.Press(t, "n")
		r.Match("日本語").WaitStyle(t, tuikit.Style{Bold: new(true), Foreground: blue})
		r.Match("alpha").WaitStyle(t, tuikit.Style{Bold: new(false)})
	})

	t.Run("a style that only some of the cells have fails the test", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, slides{menu("beta")}, tuikit.WithTimeout(50*time.Millisecond))

		msg := failure(t, func(tb testing.TB) { r.Match("> beta").WaitStyle(tb, tuikit.Style{Bold: new(true)}) })
		assert.Contains(t, msg, `gave up waiting for Match("> beta") of the screen to have Style{Bold: true} after 50ms`)
		assert.Contains(t, msg, "it has Style{} and Style{Foreground: #2b7fff, Background: #314158, Bold: true}")
		assert.Contains(t, msg, "last screen:\nmenu")
	})

	t.Run("a wrong color, or none, fails the test", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, slides{menu("beta")}, tuikit.WithTimeout(50*time.Millisecond))

		msg := failure(t, func(tb testing.TB) { r.Match("beta").WaitStyle(tb, tuikit.Style{Foreground: grey}) })
		assert.Contains(t, msg, "to have Style{Foreground: #314158}")

		msg = failure(t, func(tb testing.TB) { r.Match("alpha").WaitStyle(tb, tuikit.Style{Foreground: blue}) })
		assert.Contains(t, msg, "it has Style{}")
	})

	t.Run("a style of text that is not there fails the test", func(t *testing.T) {
		t.Parallel()
		r := tuikit.Start(t, slides{menu("beta")}, tuikit.WithTimeout(50*time.Millisecond))

		msg := failure(t, func(tb testing.TB) { r.Match("gamma").WaitStyle(tb, tuikit.Style{}) })
		assert.Contains(t, msg, "it is not on the screen")
	})
}
