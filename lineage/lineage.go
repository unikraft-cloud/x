// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package lineage draws instance family trees: an instance hangs off the
// clone source the platform reports for it, a checkpoint off the instance
// that took it.
package lineage

import (
	"cmp"
	"slices"
	"time"

	"unikraft.com/cloud/sdk/platform"
)

type Kind string

const (
	KindInstance   Kind = "instance"
	KindCheckpoint Kind = "checkpoint"
)

const DeletedPrefix = "deleted:"

type Node struct {
	Kind      Kind
	UUID      string
	Name      string
	CreatedAt time.Time
	State     platform.InstanceState
	// Source is the parent: a checkpoint's owner, or an instance's clone source.
	Source string
	Live   bool
	// Deleted marks a placeholder for a gone instance that is still a parent.
	Deleted bool
}

// Build returns the lineage forest, newest first.
func Build(
	instances []platform.Instance,
	histories []platform.GetCheckpointHistoryResponseInstanceHistory,
) []Node {
	insts := make(map[string]platform.Instance, len(instances))
	for _, inst := range instances {
		insts[inst.Uuid] = inst
	}

	// Oldest instance first, so every "first wins" below favours it deterministically.
	type hist struct {
		inst    platform.Instance
		entries []platform.CheckpointHistoryEntry
	}
	var hs []hist
	for _, h := range histories {
		inst, ok := insts[h.Uuid]
		if !ok {
			continue
		}
		entries := slices.Clone(h.History)
		slices.SortStableFunc(entries, func(a, b platform.CheckpointHistoryEntry) int {
			return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.Uuid, b.Uuid))
		})
		hs = append(hs, hist{inst: inst, entries: entries})
	}
	slices.SortStableFunc(hs, func(a, b hist) int {
		return cmp.Or(
			a.inst.CreatedAt.Compare(b.inst.CreatedAt),
			cmp.Compare(a.inst.Uuid, b.inst.Uuid),
		)
	})

	isOwn := func(inst platform.Instance, e platform.CheckpointHistoryEntry) bool {
		return e.CreatedAt.After(inst.CreatedAt)
	}

	ckpts := map[string]platform.CheckpointHistoryEntry{}
	owner := map[string]string{}
	for _, h := range hs {
		for _, e := range h.entries {
			if _, ok := ckpts[e.Uuid]; !ok {
				ckpts[e.Uuid] = e
			}
			if _, ok := owner[e.Uuid]; !ok && isOwn(h.inst, e) {
				owner[e.Uuid] = h.inst.Uuid
			}
		}
	}

	// Orphans adjacent in some history belong to the same deleted instance.
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		p, ok := parent[x]
		if !ok || p == x {
			return x
		}
		r := find(p)
		parent[x] = r
		return r
	}
	groupSource := map[string]string{}
	for _, h := range hs {
		prevOrphan, prevOwned := "", ""
		for _, e := range h.entries {
			if _, ok := owner[e.Uuid]; ok {
				prevOrphan, prevOwned = "", e.Uuid
				continue
			}
			if prevOrphan != "" {
				a, b := find(prevOrphan), find(e.Uuid)
				if a != b {
					parent[b] = a
					if s, ok := groupSource[b]; ok {
						if _, ok := groupSource[a]; !ok {
							groupSource[a] = s
						}
					}
				}
			} else if prevOwned != "" {
				if r := find(e.Uuid); groupSource[r] == "" {
					groupSource[r] = prevOwned
				}
			}
			prevOrphan = e.Uuid
		}
	}

	type group struct {
		oldest, newest platform.CheckpointHistoryEntry
	}
	groups := map[string]*group{}
	for id, e := range ckpts {
		if _, ok := owner[id]; ok {
			continue
		}
		r := find(id)
		g, ok := groups[r]
		if !ok {
			groups[r] = &group{oldest: e, newest: e}
			continue
		}
		if e.CreatedAt.Before(g.oldest.CreatedAt) ||
			(e.CreatedAt.Equal(g.oldest.CreatedAt) && e.Uuid < g.oldest.Uuid) {
			g.oldest = e
		}
		if e.CreatedAt.After(g.newest.CreatedAt) ||
			(e.CreatedAt.Equal(g.newest.CreatedAt) && e.Uuid > g.newest.Uuid) {
			g.newest = e
		}
	}

	nodes := make([]Node, 0, len(instances)+len(ckpts)+len(groups))
	for r, g := range groups {
		id := DeletedPrefix + g.newest.Uuid
		nodes = append(nodes, Node{
			Kind:      KindInstance,
			UUID:      id,
			CreatedAt: g.oldest.CreatedAt.Add(-time.Nanosecond),
			Source:    groupSource[r],
			Deleted:   true,
		})
		for cid := range ckpts {
			if _, ok := owner[cid]; !ok && find(cid) == r {
				owner[cid] = id
			}
		}
	}

	for id, e := range ckpts {
		nodes = append(nodes, Node{
			Kind:      KindCheckpoint,
			UUID:      id,
			Name:      e.Name,
			CreatedAt: e.CreatedAt,
			Source:    owner[id],
		})
	}

	// The platform reports the clone source only while it exists, so an
	// instance cloned from something gone, or from a template, is a root.
	for _, inst := range insts {
		n := Node{
			Kind:      KindInstance,
			UUID:      inst.Uuid,
			Name:      inst.Name,
			CreatedAt: inst.CreatedAt,
			State:     inst.State,
		}
		if src := inst.CloneSource; src != nil && src.Uuid != inst.Uuid {
			if _, ok := ckpts[src.Uuid]; ok {
				n.Source = src.Uuid
			} else if _, ok := insts[src.Uuid]; ok {
				n.Source, n.Live = src.Uuid, true
			}
		}
		nodes = append(nodes, n)
	}

	sortNewestFirst(nodes)
	return nodes
}

// Scope returns current's whole tree, keeping current when it trims to limit.
func Scope(nodes []Node, current string, limit int) (tree []Node, truncated bool) {
	byID := make(map[string]Node, len(nodes))
	children := map[string][]string{}
	for _, n := range nodes {
		byID[n.UUID] = n
		if n.Source != "" {
			children[n.Source] = append(children[n.Source], n.UUID)
		}
	}
	if _, ok := byID[current]; !ok {
		return nil, false
	}

	root := current
	seen := map[string]bool{root: true}
	for {
		src := byID[root].Source
		if _, ok := byID[src]; !ok || seen[src] {
			break
		}
		seen[src] = true
		root = src
	}

	in := map[string]bool{root: true}
	queue := []string{root}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		tree = append(tree, byID[id])
		for _, c := range children[id] {
			if !in[c] {
				in[c] = true
				queue = append(queue, c)
			}
		}
	}
	// A cycle (inconsistent input) has no root; keep current's own chain anyway.
	for id := range seen {
		if !in[id] {
			in[id] = true
			tree = append(tree, byID[id])
		}
	}
	sortNewestFirst(tree)

	if limit <= 0 || len(tree) <= limit {
		return tree, false
	}
	idx := slices.IndexFunc(tree, func(n Node) bool { return n.UUID == current })
	if idx < limit {
		return tree[:limit], true
	}
	return append(tree[:limit-1:limit-1], tree[idx]), true
}

func sortNewestFirst(nodes []Node) {
	slices.SortFunc(nodes, func(a, b Node) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(a.UUID, b.UUID))
	})
}
