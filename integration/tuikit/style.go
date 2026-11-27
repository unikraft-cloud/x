// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package tuikit

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"slices"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

// A Style is how text is drawn. A field that is nil is not asked for:
// Style{Bold: new(true)} is any bold text, of any color.
type Style struct {
	Foreground color.Color
	Background color.Color

	Bold          *bool
	Faint         *bool
	Italic        *bool
	Underline     *bool
	Reverse       *bool
	Strikethrough *bool
}

// WaitStyle waits for every cell of the region to be drawn as want. A cell
// that shows a space counts too, so find the words with Match first.
func (g *Region) WaitStyle(t testing.TB, want Style) {
	t.Helper()

	var got []string
	err := g.r.waitScreen(g.r.timeout, func(s screen) bool {
		bounds, ok := g.locate(s)
		if !ok {
			got = nil
			return false
		}
		got = s.styles(bounds)
		return s.styled(bounds, want)
	})
	if err == nil {
		return
	}

	has := "it is not on the screen"
	if len(got) > 0 {
		has = "it has " + strings.Join(got, " and ")
	}
	what := fmt.Sprintf("%s to have %s", g, want)
	last := g.r.last()
	if errors.Is(err, ErrStopped) {
		t.Fatalf("tuikit: waiting for %s: %v\n%s\n\nlast screen:\n%s", what, err, has, last.text(last.bounds()))
	}
	t.Fatalf("tuikit: gave up waiting for %s after %s\n%s\n\nlast screen:\n%s", what, g.r.timeout, has, last.text(last.bounds()))
}

// String writes the style as its literal, with colors as "#rrggbb".
func (st Style) String() string {
	var fields []string
	for _, attr := range st.attrs() {
		if attr.want != nil {
			fields = append(fields, fmt.Sprintf("%s: %t", attr.name, *attr.want))
		}
	}
	return literal(st.Foreground, st.Background, fields)
}

func literal(fg, bg color.Color, fields []string) string {
	if bg != nil {
		fields = slices.Insert(fields, 0, "Background: "+hex(bg))
	}
	if fg != nil {
		fields = slices.Insert(fields, 0, "Foreground: "+hex(fg))
	}
	return "Style{" + strings.Join(fields, ", ") + "}"
}

type attr struct {
	name string
	want *bool
	has  func(uv.Style) bool
}

func (st Style) attrs() []attr {
	bit := func(mask uint8) func(uv.Style) bool {
		return func(s uv.Style) bool { return s.Attrs&mask != 0 }
	}
	return []attr{
		{"Bold", st.Bold, bit(uv.AttrBold)},
		{"Faint", st.Faint, bit(uv.AttrFaint)},
		{"Italic", st.Italic, bit(uv.AttrItalic)},
		{"Underline", st.Underline, func(s uv.Style) bool { return s.Underline != uv.UnderlineNone }},
		{"Reverse", st.Reverse, bit(uv.AttrReverse)},
		{"Strikethrough", st.Strikethrough, bit(uv.AttrStrikethrough)},
	}
}

func (st Style) matches(got uv.Style) bool {
	if st.Foreground != nil && !sameColor(st.Foreground, got.Fg) {
		return false
	}
	if st.Background != nil && !sameColor(st.Background, got.Bg) {
		return false
	}
	for _, attr := range st.attrs() {
		if attr.want != nil && *attr.want != attr.has(got) {
			return false
		}
	}
	return true
}

// describe writes all of how a cell is drawn, as String does for a Style.
func describe(got uv.Style) string {
	var fields []string
	for _, attr := range (Style{}).attrs() {
		if attr.has(got) {
			fields = append(fields, attr.name+": true")
		}
	}
	return literal(got.Fg, got.Bg, fields)
}

func sameColor(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

func hex(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}

// styled reports whether every cell in bounds is drawn as want.
func (s screen) styled(bounds image.Rectangle, want Style) bool {
	if bounds.Empty() {
		return false
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for _, c := range s[y][bounds.Min.X:bounds.Max.X] {
			if c.text != "" && !want.matches(c.style) {
				return false
			}
		}
	}
	return true
}

// styles returns each way that a cell in bounds is drawn, once.
func (s screen) styles(bounds image.Rectangle) []string {
	var styles []string
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for _, c := range s[y][bounds.Min.X:bounds.Max.X] {
			if st := describe(c.style); c.text != "" && !slices.Contains(styles, st) {
				styles = append(styles, st)
			}
		}
	}
	return styles
}
