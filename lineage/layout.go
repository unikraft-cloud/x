// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package lineage

import (
	"math"
	"slices"
)

// ForkPoint is how far below a live fork's row its branch leaves the parent's line.
const ForkPoint = 0.55

// Span is an instance's row together with the rows of its own checkpoints.
type Span struct {
	Owner string
	Lane  int
	// Color is the span's index in Layout.Spans; unlike Lane it is unique.
	Color       int
	Top, Bottom int
	// End is where the line stops at the top: Top, or lower down where a live fork leaves.
	End float64
}

type Layout struct {
	Rows      []Node
	Lane      map[string]int
	Color     map[string]int
	RowOf     map[string]int
	Spans     []Span
	LaneCount int
}

func Compute(nodes []Node) Layout {
	rows := slices.Clone(nodes)

	// Bounded so that inconsistent input cannot loop.
	sortNewestFirst(rows)
	for range len(rows) {
		if !liftChildren(rows) {
			break
		}
	}

	l := Layout{
		Rows:  rows,
		Lane:  make(map[string]int, len(rows)),
		Color: make(map[string]int, len(rows)),
		RowOf: make(map[string]int, len(rows)),
	}
	for i, n := range rows {
		l.RowOf[n.UUID] = i
	}

	spanOf := map[string]int{}
	for i, n := range rows {
		o := owner(n)
		if k, ok := spanOf[o]; ok {
			l.Spans[k].Bottom = i
			continue
		}
		spanOf[o] = len(l.Spans)
		l.Spans = append(l.Spans, Span{Owner: o, Top: i, Bottom: i, End: float64(i)})
	}

	for i, n := range rows {
		if !n.Live {
			continue
		}
		if _, ok := l.RowOf[n.Source]; !ok {
			continue
		}
		s := &l.Spans[spanOf[n.Source]]
		s.End = min(s.End, float64(i)+ForkPoint)
	}

	// A span claims rows down to its parent too, as its branch must cross no dot.
	slices.SortFunc(l.Spans, func(a, b Span) int { return b.Bottom - a.Bottom })
	byLane := [][]Span{}
	for k := range l.Spans {
		s := &l.Spans[k]
		parent, end := -1, s.Bottom
		if o, ok := l.RowOf[s.Owner]; ok && !rows[o].Live {
			if p, ok := l.RowOf[rows[o].Source]; ok && p > s.Bottom {
				parent, end = p, p-1
			}
		}
		top := lineTop(*s)
		free := func(placed []Span) bool {
			for _, q := range placed {
				qTop := lineTop(q)
				if q.Top == parent && qTop == q.Top {
					// The branch continues q's line straight up, as in git log.
					if q.Top < s.Bottom+2 {
						return false
					}
					continue
				}
				if qTop <= end+1 && q.Bottom >= top-1 {
					return false
				}
			}
			return true
		}
		lane := slices.IndexFunc(byLane, free)
		if lane < 0 {
			lane = len(byLane)
			byLane = append(byLane, nil)
		}
		s.Lane = lane
		s.Color = k
		byLane[lane] = append(byLane[lane], *s)
		spanOf[s.Owner] = k
	}
	l.LaneCount = len(byLane)

	for _, n := range rows {
		s := l.Spans[spanOf[owner(n)]]
		l.Lane[n.UUID] = s.Lane
		l.Color[n.UUID] = s.Color
	}
	return l
}

func lineTop(s Span) int {
	return int(math.Floor(s.End))
}

func owner(n Node) string {
	if n.Kind == KindCheckpoint && n.Source != "" {
		return n.Source
	}
	return n.UUID
}

// liftChildren moves every node that sorts below its parent to directly above it.
func liftChildren(rows []Node) bool {
	moved := false
	for i := range rows {
		src := rows[i].Source
		if src == "" {
			continue
		}
		p := slices.IndexFunc(rows[:i], func(n Node) bool { return n.UUID == src })
		if p < 0 {
			continue
		}
		child := rows[i]
		copy(rows[p+1:i+1], rows[p:i])
		rows[p] = child
		moved = true
	}
	return moved
}
