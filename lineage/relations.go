// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package lineage

func Find(nodes []Node, uuid string) (Node, bool) {
	for _, n := range nodes {
		if n.UUID == uuid {
			return n, true
		}
	}
	return Node{}, false
}

func CheckpointsOf(nodes []Node, uuid string) []Node {
	var out []Node
	for _, n := range nodes {
		if n.Kind == KindCheckpoint && n.Source == uuid {
			out = append(out, n)
		}
	}
	return out
}

// ForksOf includes forks of the instance's own checkpoints.
func ForksOf(nodes []Node, uuid string) []Node {
	from := map[string]bool{uuid: true}
	for _, n := range CheckpointsOf(nodes, uuid) {
		from[n.UUID] = true
	}
	var out []Node
	for _, n := range nodes {
		if n.Kind == KindInstance && n.Source != "" && from[n.Source] {
			out = append(out, n)
		}
	}
	return out
}
