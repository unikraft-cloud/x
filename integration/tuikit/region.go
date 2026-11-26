// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package tuikit

import (
	"errors"
	"fmt"
	"image"
	"regexp"
	"strings"
	"testing"
)

// A Region is a rectangle of the screen. It is found again each time it is
// used, so it follows the screen as the program draws, and it can name a part
// of the screen that is not there yet.
//
// A Runner is the Region of its whole screen. Line, Lines, Match, LineMatch,
// and Inset find a Region inside another.
type Region struct {
	r      *Runner
	parent *Region
	name   string
	find   func(s screen, in image.Rectangle) (image.Rectangle, bool)
}

// Line returns line n of the region. Lines count from 0, and a negative n
// counts from the last line, which is -1.
func (g *Region) Line(n int) *Region {
	return g.Lines(n, n).named(fmt.Sprintf("Line(%d)", n))
}

// Lines returns the lines from and to of the region, and those between them.
// They count as in Line: Lines(1, -2) leaves out the first and the last.
func (g *Region) Lines(from, to int) *Region {
	return g.child(fmt.Sprintf("Lines(%d, %d)", from, to), func(_ screen, in image.Rectangle) (image.Rectangle, bool) {
		first, last := index(from, in.Dy()), index(to, in.Dy())
		if first < 0 || last >= in.Dy() || first > last {
			return image.Rectangle{}, false
		}
		return image.Rect(in.Min.X, in.Min.Y+first, in.Max.X, in.Min.Y+last+1), true
	})
}

// Match returns the cells of the first place in the region that shows text,
// on one line.
func (g *Region) Match(text string) *Region {
	return g.child(fmt.Sprintf("Match(%q)", text), matchText(text, false))
}

// MatchRegexp returns the cells of the first place in the region that the
// regular expression pattern matches, on one line. It panics if pattern is
// not a regular expression.
func (g *Region) MatchRegexp(pattern string) *Region {
	return g.child(fmt.Sprintf("MatchRegexp(%q)", pattern), matchRegexp(pattern, false))
}

// LineMatch returns the first line of the region that shows text: all of the
// line, where Match returns only the cells of text.
func (g *Region) LineMatch(text string) *Region {
	return g.child(fmt.Sprintf("LineMatch(%q)", text), matchText(text, true))
}

// LineMatchRegexp returns the first line of the region that the regular
// expression pattern matches, as LineMatch does for text. It panics if
// pattern is not a regular expression.
func (g *Region) LineMatchRegexp(pattern string) *Region {
	return g.child(fmt.Sprintf("LineMatchRegexp(%q)", pattern), matchRegexp(pattern, true))
}

// Inset returns the region without the n lines and columns at each of its
// edges: Inset(1) of a box is what its border surrounds. A region only gets
// smaller: a negative n finds nothing.
func (g *Region) Inset(n int) *Region {
	return g.child(fmt.Sprintf("Inset(%d)", n), func(_ screen, in image.Rectangle) (image.Rectangle, bool) {
		inner := in.Inset(n)
		return inner, n >= 0 && !inner.Empty()
	})
}

// Exists reports whether the region is on the screen now.
func (g *Region) Exists() bool {
	_, ok := g.Bounds()
	return ok
}

// Bounds returns where the region is on the screen now, in columns and
// lines from the top left, and false if it is not there.
func (g *Region) Bounds() (image.Rectangle, bool) {
	return g.locate(g.r.screen())
}

// Snapshot returns the text of the region, without the trailing spaces of
// each line or the empty lines at the bottom. It is empty for a region that
// is not on the screen.
func (g *Region) Snapshot() string {
	s := g.r.screen()
	bounds, ok := g.locate(s)
	if !ok {
		return ""
	}
	return s.text(bounds)
}

// Wait waits for cond to accept the region, and returns the text it
// accepted. cond gets the text of the region as Snapshot returns it, and
// whether the region exists. It fails the test with the last screen if cond
// never accepts.
func (g *Region) Wait(t testing.TB, cond func(text string, exists bool) bool) string {
	t.Helper()

	return g.wait(t, "a condition on "+g.String(), cond)
}

// WaitExists waits for the region to be on the screen, and returns its text.
func (g *Region) WaitExists(t testing.TB) string {
	t.Helper()

	return g.wait(t, g.String()+" to exist", func(_ string, exists bool) bool { return exists })
}

// WaitMatch waits for the region to have text, and returns the text of
// the region.
func (g *Region) WaitMatch(t testing.TB, text string) string {
	t.Helper()

	return g.wait(t, fmt.Sprintf("%s to contain %q", g, text), func(got string, exists bool) bool {
		return exists && strings.Contains(got, text)
	})
}

// WaitNotMatch waits for the region to be on the screen without text, and
// returns the text of the region. A region that does not exist is not one
// without text: it still waits for it.
func (g *Region) WaitNotMatch(t testing.TB, text string) string {
	t.Helper()

	return g.wait(t, fmt.Sprintf("%s to not contain %q", g, text), func(got string, exists bool) bool {
		return exists && !strings.Contains(got, text)
	})
}

// WaitMatchRegexp waits for the region to have text that the regular
// expression pattern matches, and returns the text of the region.
func (g *Region) WaitMatchRegexp(t testing.TB, pattern string) string {
	t.Helper()

	return g.waitRegexp(t, pattern, "to match", true)
}

// WaitNotMatchRegexp waits for the region to be on the screen with no text
// that pattern matches, as WaitNotMatch does for text.
func (g *Region) WaitNotMatchRegexp(t testing.TB, pattern string) string {
	t.Helper()

	return g.waitRegexp(t, pattern, "to not match", false)
}

func (g *Region) waitRegexp(t testing.TB, pattern, what string, want bool) string {
	t.Helper()

	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("tuikit: %v", err)
	}
	return g.wait(t, fmt.Sprintf("%s %s %q", g, what, pattern), func(got string, exists bool) bool {
		return exists && re.MatchString(got) == want
	})
}

// String describes how the region is found: "Line(0) of Inset(1) of the screen".
func (g *Region) String() string {
	if g.parent == nil {
		return "the screen"
	}
	return g.name + " of " + g.parent.String()
}

func (g *Region) wait(t testing.TB, what string, cond func(text string, exists bool) bool) string {
	t.Helper()

	var text string
	err := g.r.waitScreen(g.r.timeout, func(s screen) bool {
		bounds, ok := g.locate(s)
		text = ""
		if ok {
			text = s.text(bounds)
		}
		return cond(text, ok)
	})
	last := g.r.last()
	if errors.Is(err, ErrStopped) {
		t.Fatalf("tuikit: waiting for %s: %v\n\nlast screen:\n%s", what, err, last.text(last.bounds()))
	}
	if err != nil {
		t.Fatalf("tuikit: gave up waiting for %s after %s\n\nlast screen:\n%s", what, g.r.timeout, last.text(last.bounds()))
	}

	return text
}

func (g *Region) child(name string, find func(screen, image.Rectangle) (image.Rectangle, bool)) *Region {
	return &Region{r: g.r, parent: g, name: name, find: find}
}

func (g *Region) named(name string) *Region {
	g.name = name
	return g
}

// locate finds the region on s, inside the regions it was found in.
func (g *Region) locate(s screen) (image.Rectangle, bool) {
	if g.parent == nil {
		return s.bounds(), true
	}
	in, ok := g.parent.locate(s)
	if !ok {
		return image.Rectangle{}, false
	}
	return g.find(s, in)
}

// index returns line n of size lines, where a negative n counts from the end.
func index(n, size int) int {
	if n < 0 {
		return size + n
	}
	return n
}

// screen is what a terminal shows at one moment: its cells, by line and then
// by column. The cells that a wide character covers after its first have no
// text.
type screen [][]cell

type cell struct {
	text string
}

func (s screen) bounds() image.Rectangle {
	if len(s) == 0 {
		return image.Rectangle{}
	}
	return image.Rect(0, 0, len(s[0]), len(s))
}

// text returns the text of the cells in bounds, without the trailing spaces
// of each line or the empty lines at the bottom.
func (s screen) text(bounds image.Rectangle) string {
	lines := make([]string, 0, bounds.Dy())
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		lines = append(lines, strings.TrimRight(s.join(y, bounds.Min.X, bounds.Max.X), " "))
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// join returns the text of line y from column from up to column to.
func (s screen) join(y, from, to int) string {
	var b strings.Builder
	for _, c := range s[y][from:to] {
		b.WriteString(c.text)
	}
	return b.String()
}

type finder = func(s screen, in image.Rectangle) (image.Rectangle, bool)

func matchText(text string, line bool) finder {
	return func(s screen, in image.Rectangle) (image.Rectangle, bool) {
		return s.find(in, line, func(got string) (int, int) {
			i := strings.Index(got, text)
			return i, i + len(text)
		})
	}
}

func matchRegexp(pattern string, line bool) finder {
	re := regexp.MustCompile(pattern)
	return func(s screen, in image.Rectangle) (image.Rectangle, bool) {
		return s.find(in, line, func(got string) (int, int) {
			if loc := re.FindStringIndex(got); loc != nil {
				return loc[0], loc[1]
			}
			return -1, -1
		})
	}
}

// find returns the first line in bounds that match accepts, or with line
// false, only the cells of it that match accepts. match gets the text of a
// line, and returns the bytes of it that it accepts, or a negative start.
func (s screen) find(bounds image.Rectangle, line bool, match func(got string) (start, end int)) (image.Rectangle, bool) {
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		start, end := match(strings.TrimRight(s.join(y, bounds.Min.X, bounds.Max.X), " "))
		if start < 0 || start >= end {
			continue
		}
		from, to, at := bounds.Min.X, bounds.Max.X, 0
		for x := bounds.Min.X; x < bounds.Max.X && !line; x++ {
			if at <= start && s[y][x].text != "" {
				from = x
			}
			if at >= end && s[y][x].text != "" {
				to = x
				break
			}
			at += len(s[y][x].text)
		}
		return image.Rect(from, y, to, y+1), true
	}
	return image.Rectangle{}, false
}
