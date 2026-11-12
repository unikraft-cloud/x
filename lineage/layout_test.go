// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package lineage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unikraft.com/cloud/sdk/platform"
)

func lanes(l Layout) []int {
	out := make([]int, len(l.Rows))
	for i, n := range l.Rows {
		out[i] = l.Lane[n.UUID]
	}
	return out
}

func specTree(t *testing.T) []Node {
	t.Helper()
	tree, _ := Scope(Build(specFixture()), "http-gcc", 0)
	require.NotNil(t, tree)
	return tree
}

func TestComputeSpec(t *testing.T) {
	l := Compute(specTree(t))

	assert.Equal(t, []string{
		"http-gcc-fork-1-debug", // 0: lane 0, own span
		"fork-1-ckpt-1",         // 1: lane 1, fork-1's span
		"ckpt-3",                // 2: lane 0, http-gcc's span
		"http-gcc-fork-1",       // 3: lane 1
		"ckpt-2",                // 4: lane 0
		"ckpt-1",                // 5: lane 0
		"http-gcc",              // 6: lane 0
	}, uuids(l.Rows))
	assert.Equal(t, []int{0, 1, 0, 1, 0, 0, 0}, lanes(l))
	assert.Equal(t, []Span{
		{Owner: "http-gcc", Lane: 0, Color: 0, Top: 2, Bottom: 6, End: 2},
		{Owner: "http-gcc-fork-1", Lane: 1, Color: 1, Top: 1, Bottom: 3, End: 1},
		{Owner: "http-gcc-fork-1-debug", Lane: 0, Color: 2, Top: 0, Bottom: 0, End: 0},
	}, l.Spans)
	assert.Equal(t, 2, l.LaneCount)
	assertBranchesClear(t, l)

	// Color follows the span, not the lane.
	assert.Equal(t, 2, l.Color["http-gcc-fork-1-debug"])
	assert.Equal(t, 0, l.Color["ckpt-3"])
	assert.Equal(t, 1, l.Color["fork-1-ckpt-1"])
	for i, n := range l.Rows {
		assert.Equal(t, i, l.RowOf[n.UUID])
	}
}

func TestComputeSingleRoot(t *testing.T) {
	l := Compute(Build([]platform.Instance{inst("solo", 0)}, nil))

	assert.Equal(t, []string{"solo"}, uuids(l.Rows))
	assert.Equal(t, []int{0}, lanes(l))
	assert.Equal(t, []Span{{Owner: "solo"}}, l.Spans)
	assert.Equal(t, 1, l.LaneCount)
}

func TestComputeLaneReuseNeedsGap(t *testing.T) {
	l := Compute(Build(
		[]platform.Instance{inst("a", 0), inst("c", 15), inst("b", 20)},
		[]platform.GetCheckpointHistoryResponseInstanceHistory{
			history("a", ckpt("a1", 10)),
			history("b", ckpt("b1", 30)),
		},
	))

	assert.Equal(t, []string{"b1", "b", "c", "a1", "a"}, uuids(l.Rows))
	assert.Equal(t, []int{0, 0, 1, 0, 0}, lanes(l))
	assert.Equal(t, 2, l.LaneCount)
	assert.Equal(t, []string{"a", "c", "b"}, []string{
		l.Spans[0].Owner, l.Spans[1].Owner, l.Spans[2].Owner,
	})
}

func TestComputeLiftsChildAboveParent(t *testing.T) {
	// Clock skew: a checkpoint timestamped before its owner.
	l := Compute([]Node{
		{Kind: KindInstance, UUID: "x", CreatedAt: at(10)},
		{Kind: KindInstance, UUID: "y", CreatedAt: at(7)},
		{Kind: KindCheckpoint, UUID: "k", Source: "x", CreatedAt: at(5)},
	})

	assert.Equal(t, []string{"k", "x", "y"}, uuids(l.Rows))
}

func TestComputeCycleTerminates(t *testing.T) {
	l := Compute([]Node{
		{Kind: KindCheckpoint, UUID: "c", Source: "i", CreatedAt: at(0)},
		{Kind: KindInstance, UUID: "i", Source: "c", CreatedAt: at(10)},
	})

	assert.Len(t, l.Rows, 2)
}

// assertBranchesClear fails if a branch passes another span's dot in its lane.
func assertBranchesClear(t *testing.T, l Layout) {
	t.Helper()
	for _, s := range l.Spans {
		p, ok := l.RowOf[l.Rows[l.RowOf[s.Owner]].Source]
		if !ok {
			continue
		}
		for r := s.Top + 1; r < p; r++ {
			n := l.Rows[r]
			if l.Color[n.UUID] != s.Color && l.Lane[n.UUID] == s.Lane {
				assert.Failf(t, "branch crosses a dot", "branch to %s in lane %d passes %s (row %d)", s.Owner, s.Lane, n.UUID, r)
			}
		}
	}
}

func TestComputeSiblingForksGetOwnLanes(t *testing.T) {
	// Lane 0 continues root's line; any other shared lane would cross a sibling's dot.
	l := Compute([]Node{
		{Kind: KindInstance, UUID: "root", CreatedAt: at(0)},
		{Kind: KindCheckpoint, UUID: "ckpt", Source: "root", CreatedAt: at(10)},
		{Kind: KindInstance, UUID: "f1", Source: "ckpt", CreatedAt: at(20)},
		{Kind: KindInstance, UUID: "f2", Source: "ckpt", CreatedAt: at(30)},
		{Kind: KindInstance, UUID: "f3", Source: "ckpt", CreatedAt: at(40)},
		{Kind: KindInstance, UUID: "f4", Source: "ckpt", CreatedAt: at(50)},
	})

	assert.Equal(t, []string{"f4", "f3", "f2", "f1", "ckpt", "root"}, uuids(l.Rows))
	assert.Equal(t, []int{3, 2, 0, 1, 0, 0}, lanes(l))
	assert.Equal(t, 4, l.LaneCount)
	assertBranchesClear(t, l)
}

func TestComputeBranchSkipsNewerCheckpoint(t *testing.T) {
	// g's branch from c1 would run through c2 in lane 0, and through f in lane 1.
	l := Compute([]Node{
		{Kind: KindInstance, UUID: "root", CreatedAt: at(0)},
		{Kind: KindCheckpoint, UUID: "c1", Source: "root", CreatedAt: at(10)},
		{Kind: KindCheckpoint, UUID: "c2", Source: "root", CreatedAt: at(20)},
		{Kind: KindInstance, UUID: "f", Source: "c2", CreatedAt: at(25)},
		{Kind: KindInstance, UUID: "g", Source: "c1", CreatedAt: at(30)},
	})

	assert.Equal(t, []string{"g", "f", "c2", "c1", "root"}, uuids(l.Rows))
	assert.Equal(t, []int{2, 1, 0, 0, 0}, lanes(l))
	assertBranchesClear(t, l)
}

func TestComputeSpecLiveFork(t *testing.T) {
	tree, _ := Scope(Build(liveFixture()), "http-gcc", 0)
	l := Compute(tree)

	// http-gcc's line already runs past the fork, so its End stays at its Top.
	assert.Equal(t, []string{
		"http-gcc-fork-1-debug", // 0: lane 0
		"fork-1-ckpt-1",         // 1: lane 1
		"ckpt-3",                // 2: lane 0
		"http-gcc-fork-1",       // 3: lane 1
		"ckpt-2",                // 4: lane 0
		"http-gcc-staging",      // 5: lane 1, forked live
		"ckpt-1",                // 6: lane 0
		"http-gcc",              // 7: lane 0
	}, uuids(l.Rows))
	assert.Equal(t, []int{0, 1, 0, 1, 0, 1, 0, 0}, lanes(l))
	assert.Equal(t, []Span{
		{Owner: "http-gcc", Lane: 0, Color: 0, Top: 2, Bottom: 7, End: 2},
		{Owner: "http-gcc-staging", Lane: 1, Color: 1, Top: 5, Bottom: 5, End: 5},
		{Owner: "http-gcc-fork-1", Lane: 1, Color: 2, Top: 1, Bottom: 3, End: 1},
		{Owner: "http-gcc-fork-1-debug", Lane: 0, Color: 3, Top: 0, Bottom: 0, End: 0},
	}, l.Spans)
	assertBranchesClear(t, l)
}

func TestComputeLiveForkExtendsParentLine(t *testing.T) {
	l := Compute([]Node{
		{Kind: KindInstance, UUID: "root", CreatedAt: at(0)},
		{Kind: KindInstance, UUID: "f", Source: "root", Live: true, CreatedAt: at(10)},
	})

	assert.Equal(t, []string{"f", "root"}, uuids(l.Rows))
	assert.Equal(t, []Span{
		{Owner: "root", Lane: 0, Color: 0, Top: 1, Bottom: 1, End: ForkPoint},
		{Owner: "f", Lane: 1, Color: 1, Top: 0, Bottom: 0, End: 0},
	}, l.Spans)
	assert.Equal(t, 2, l.LaneCount)
}
