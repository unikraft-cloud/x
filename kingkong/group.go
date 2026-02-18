// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package kingkong

import (
	"slices"

	"github.com/alecthomas/kong"
)

const tagCollapse = "collapse"

type FlagGroup struct {
	Metadata *kong.Group
	Flags    [][]*kong.Flag
}

func GroupFlags(flags [][]*kong.Flag) []FlagGroup {
	// Group keys in order of appearance.
	groups := []*kong.Group{}
	// Flags grouped by their group key.
	flagsByGroup := map[string][][]*kong.Flag{}

	for _, levelFlags := range flags {
		levelFlags = collapseFlags(levelFlags)

		levelFlagsByGroup := map[string][]*kong.Flag{}

		for _, flag := range levelFlags {
			key := ""
			if flag.Flag.Name == "help" && flag.Group == nil {
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

		for key, flags := range levelFlagsByGroup {
			flagsByGroup[key] = append(flagsByGroup[key], flags)
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

func collapseFlags(flags []*kong.Flag) []*kong.Flag {
	out := make([]*kong.Flag, 0, len(flags))
	byID := map[string]*kong.Flag{}

	for _, flag := range flags {
		if flag == nil {
			continue
		}
		if flag.Value == nil || flag.Tag == nil {
			out = append(out, flag)
			continue
		}

		id := flag.Tag.Get(tagCollapse)
		if id == "" {
			out = append(out, flag)
			continue
		}

		base := byID[id]
		if base == nil {
			clone := new(kong.Flag)
			*clone = *flag
			clone.Aliases = append([]string(nil), flag.Aliases...)
			byID[id] = clone
			out = append(out, clone)
			continue
		}

		appendUniqueAlias(base, flag.Name)
	}

	return out
}

func appendUniqueAlias(flag *kong.Flag, name string) {
	if flag == nil || name == "" || name == flag.Name {
		return
	}
	if slices.Contains(flag.Aliases, name) {
		return
	}
	flag.Aliases = append(flag.Aliases, name)
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
