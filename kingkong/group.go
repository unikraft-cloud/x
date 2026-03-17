// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package kingkong

import (
	"github.com/alecthomas/kong"
)

const tagCollapse = "collapse"

// Flag wraps kong.Flag and keeps collapsed flag variants.
type Flag struct {
	*kong.Flag
	Collapsed []*kong.Flag
}

type FlagGroup struct {
	Metadata *kong.Group
	Flags    [][]*Flag
}

func GroupFlags(flags [][]*kong.Flag) []FlagGroup {
	// Group keys in order of appearance.
	groups := []*kong.Group{}
	// Flags grouped by their group key.
	flagsByGroup := map[string][][]*Flag{}

	for _, levelFlags := range flags {
		collapsedFlags := collapseFlags(levelFlags)

		levelFlagsByGroup := map[string][]*Flag{}

		for _, flag := range collapsedFlags {
			key := ""
			if flag.Name == "help" && flag.Group == nil {
				flag.Group = &kong.Group{
					Key: "flag-global",
				}
			}
			if flag.Group != nil {
				key = flag.Group.Key
				groupAlreadySeen := false
				for _, group := range groups {
					if key == group.Key {
						groupAlreadySeen = true
						break
					}
				}
				if !groupAlreadySeen {
					groups = append(groups, flag.Group)
				}
			}

			levelFlagsByGroup[key] = append(levelFlagsByGroup[key], flag)
		}

		for key, groupedFlags := range levelFlagsByGroup {
			flagsByGroup[key] = append(flagsByGroup[key], groupedFlags)
		}
	}

	out := []FlagGroup{}
	// Ungrouped flags are always displayed first.
	if ungroupedFlags, ok := flagsByGroup[""]; ok {
		out = append(out, FlagGroup{
			Metadata: &kong.Group{
				Title: Underline("Flags") + ":",
			},
			Flags: ungroupedFlags,
		})
	}
	for _, group := range groups {
		out = append(out, FlagGroup{Metadata: group, Flags: flagsByGroup[group.Key]})
	}
	return out
}

func collapseFlags(flags []*kong.Flag) []*Flag {
	out := make([]*Flag, 0, len(flags))
	byID := map[string]*Flag{}

	for _, flag := range flags {
		if flag == nil {
			continue
		}
		if flag.Value == nil || flag.Tag == nil {
			out = append(out, &Flag{Flag: flag})
			continue
		}

		id := flag.Tag.Get(tagCollapse)
		if id == "" {
			out = append(out, &Flag{Flag: flag})
			continue
		}

		base := byID[id]
		if base == nil {
			clone := new(kong.Flag)
			*clone = *flag
			clone.Aliases = append([]string(nil), flag.Aliases...)
			wrapped := &Flag{Flag: clone}
			byID[id] = wrapped
			out = append(out, wrapped)
			continue
		}

		base.Collapsed = append(base.Collapsed, flag)
	}

	return out
}

type CommandGroup struct {
	Metadata *kong.Group
	Commands []*kong.Node
}

func GroupCommands(nodes []*kong.Node) []CommandGroup {
	// Groups in order of appearance.
	groups := []*kong.Group{}
	// Nodes grouped by their group key.
	nodesByGroup := map[string][]*kong.Node{}

	for _, node := range nodes {
		key := ""
		if group := node.ClosestGroup(); group != nil {
			key = group.Key
			if _, ok := nodesByGroup[key]; !ok {
				groups = append(groups, group)
			}
		}
		nodesByGroup[key] = append(nodesByGroup[key], node)
	}

	out := []CommandGroup{}
	// Ungrouped nodes are always displayed first.
	if ungroupedNodes, ok := nodesByGroup[""]; ok {
		out = append(out, CommandGroup{
			Metadata: &kong.Group{
				Title: Underline("Commands") + ":",
			},
			Commands: ungroupedNodes,
		})
	}
	for _, group := range groups {
		out = append(out, CommandGroup{Metadata: group, Commands: nodesByGroup[group.Key]})
	}
	return out
}
