// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package tuikit

import (
	"fmt"
	"image"
	"strings"
	"unicode/utf8"
)

// A Heuristic finds a region from how the screen looks, which the program
// has no say in: it is a guess, and it can find the wrong thing. Box is the
// only one.
type Heuristic interface {
	find(s screen, in image.Rectangle) (image.Rectangle, bool)
	name() string
}

// Box finds a rectangle that is drawn with border characters, border
// included. The zero Box finds the smallest box there is.
//
// When several boxes fit, it is the smallest, and then the first from the
// top left.
type Box struct {
	// Match is text that a line inside the box has.
	Match string

	// Line is the line inside the box that has Match, or that must be
	// there if Match is empty. Lines count as in Region.Line, without the
	// border: new(0) is the first line under the top border, and new(-1) the
	// last. Any line will do if it is nil.
	Line *int

	// Label is text that takes the place of a part of the top or the bottom
	// border, such as a title. A border may only be broken like this by its
	// Label: a box with a caption is not found without one.
	Label string

	// Chars are the characters of the border. The default is ASCII (+, - and
	// |) and the Unicode box drawing characters, where a corner has to be a
	// corner. With Chars, any of them can be anywhere in the border.
	Chars string
}

func (b Box) name() string {
	var fields []string
	if b.Match != "" {
		fields = append(fields, fmt.Sprintf("Match: %q", b.Match))
	}
	if b.Line != nil {
		fields = append(fields, fmt.Sprintf("Line: %d", *b.Line))
	}
	if b.Label != "" {
		fields = append(fields, fmt.Sprintf("Label: %q", b.Label))
	}
	if b.Chars != "" {
		fields = append(fields, fmt.Sprintf("Chars: %q", b.Chars))
	}
	return "Box{" + strings.Join(fields, ", ") + "}"
}

func (b Box) find(s screen, in image.Rectangle) (image.Rectangle, bool) {
	corner, horizontal, vertical := b.borders()

	// down[y][x] is how many cells from (x, y) down can be a side of a box.
	down := make([][]int, in.Max.Y+1)
	down[in.Max.Y] = make([]int, in.Max.X)
	for y := in.Max.Y - 1; y >= in.Min.Y; y-- {
		down[y] = make([]int, in.Max.X)
		for x := in.Min.X; x < in.Max.X; x++ {
			if vertical(s[y][x].text) {
				down[y][x] = down[y+1][x] + 1
			}
		}
	}

	var (
		best  image.Rectangle
		found bool
	)
	for top := in.Min.Y; top < in.Max.Y-2; top++ {
		for left := in.Min.X; left < in.Max.X-2; left++ {
			if !corner(s[top][left].text) {
				continue
			}
			for bottom := top + 2; bottom <= top+1+down[top+1][left] && bottom < in.Max.Y; bottom++ {
				if !corner(s[bottom][left].text) {
					continue
				}
				for right := left + 2; right < in.Max.X; right++ {
					box := image.Rect(left, top, right+1, bottom+1)
					if found && area(box) >= area(best) {
						break
					}
					if !corner(s[top][right].text) || !corner(s[bottom][right].text) || down[top+1][right] < bottom-top-1 {
						continue
					}
					if b.edges(s, horizontal, box) && b.contains(s, box) {
						best, found = box, true
					}
				}
			}
		}
	}

	return best, found
}

// edges reports whether the top and the bottom of box are borders: both
// unbroken, or with a Label, one of them broken by it.
func (b Box) edges(s screen, horizontal func(string) bool, box image.Rectangle) bool {
	top := b.edge(s, horizontal, box.Min.Y, box.Min.X+1, box.Max.X-1)
	bottom := b.edge(s, horizontal, box.Max.Y-1, box.Min.X+1, box.Max.X-1)
	if b.Label == "" {
		return top == unbroken && bottom == unbroken
	}
	return (top == labelled && bottom == unbroken) || (top == unbroken && bottom == labelled)
}

type edge int

const (
	notAnEdge edge = iota
	unbroken
	labelled
)

// edge looks at the cells of line y from column from up to column to.
func (b Box) edge(s screen, horizontal func(string) bool, y, from, to int) edge {
	first, last := -1, -1
	for x := from; x < to; x++ {
		if horizontal(s[y][x].text) {
			continue
		}
		if first < 0 {
			first = x
		}
		last = x
	}
	if first < 0 {
		return unbroken
	}
	// A label has border on both of its sides, and is one run of text.
	if b.Label == "" || first == from || last == to-1 {
		return notAnEdge
	}
	text := s.join(y, first, last+1)
	for x := first; x <= last; x++ {
		if horizontal(s[y][x].text) && !strings.Contains(b.Label, s[y][x].text) {
			return notAnEdge
		}
	}
	if !strings.Contains(text, b.Label) {
		return notAnEdge
	}
	return labelled
}

// contains reports whether the inside of box has Match, on its Line.
func (b Box) contains(s screen, box image.Rectangle) bool {
	inside := box.Inset(1)
	if b.Line != nil {
		n := index(*b.Line, inside.Dy())
		if n < 0 || n >= inside.Dy() {
			return false
		}
		inside = image.Rect(inside.Min.X, inside.Min.Y+n, inside.Max.X, inside.Min.Y+n+1)
	}
	for y := inside.Min.Y; y < inside.Max.Y; y++ {
		if strings.Contains(s.join(y, inside.Min.X, inside.Max.X), b.Match) {
			return true
		}
	}
	return false
}

// borders returns what a cell has to be to be a corner, a part of the top or
// the bottom, and a part of a side.
func (b Box) borders() (corner, horizontal, vertical func(cell string) bool) {
	if b.Chars != "" {
		member := func(cell string) bool { return cell != "" && strings.Contains(b.Chars, cell) }
		return member, member, member
	}

	is := func(chars string) func(string) bool {
		return func(cell string) bool {
			r, size := utf8.DecodeRuneInString(cell)
			if size == 0 || size != len(cell) {
				return false
			}
			// The tees and the crosses, which are where two boxes share a border.
			if r == '+' || (r >= '├' && r <= '╋') || (r >= '╞' && r <= '╬') {
				return true
			}
			return strings.ContainsRune(chars, r)
		}
	}
	return is("┌┍┎┏┐┑┒┓└┕┖┗┘┙┚┛╒╓╔╕╖╗╘╙╚╛╜╝╭╮╯╰"), is("-=─━┄┅┈┉╌╍═"), is("|│┃┆┇┊┋╎╏║")
}

func area(r image.Rectangle) int {
	return r.Dx() * r.Dy()
}
