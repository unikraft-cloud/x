// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package builtins answers a session's ":" lines from a kong grammar, so that a
// platform declares its builtins as commands rather than implementing a
// [shell.Builtin] for each.
package builtins

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/alecthomas/kong"

	"unikraft.com/x/shell"
)

// KongConfig is the grammar a platform answers with, and what its commands run against.
type KongConfig struct {
	// Commands returns a fresh grammar, as kong writes the parsed line into it.
	// Every command is named with the sigil, as ":mount".
	Commands func() any

	// Options are the platform's own kong options: its description, its
	// variables, its mappers.
	Options []kong.Option

	// Bind is what the commands take beyond the context: the platform's own
	// stdio built from the streams, the receiver they hang off.
	Bind func(ctx context.Context, streams shell.Streams) []any

	// Restart reports a builtin that takes the instance down and up again, by
	// its argv. A platform without one leaves this nil.
	Restart func(args []string) bool
}

// Kong is a grammar's worth of builtins.
type Kong struct {
	cfg   KongConfig
	nodes []*kong.Node
}

func NewKong(cfg KongConfig) (*Kong, error) {
	if cfg.Commands == nil {
		return nil, errors.New("no builtin commands to answer with")
	}

	parser, err := kong.New(cfg.Commands(), cfg.options(io.Discard, io.Discard)...)
	if err != nil {
		return nil, err
	}

	nodes := slices.Clone(parser.Model.Children)
	slices.SortFunc(nodes, func(a, b *kong.Node) int { return strings.Compare(a.Name, b.Name) })

	return &Kong{cfg: cfg, nodes: nodes}, nil
}

func (c KongConfig) options(out, err io.Writer) []kong.Option {
	return append([]kong.Option{
		kong.Name(""),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true, FlagsLast: true}),
		kong.Writers(out, err),
		kong.Exit(func(int) {}),
	}, c.Options...)
}

// Builtins is one [shell.Builtin] per command of the grammar, by name, each
// also answering as [shell.Restarts] with what the platform's Restart says.
func (k *Kong) Builtins() map[string]shell.Builtin {
	builtins := make(map[string]shell.Builtin, len(k.nodes))
	for _, node := range k.nodes {
		name := strings.TrimPrefix(node.Name, shell.BuiltinMarker)
		builtins[name] = builtin{k: k, name: name}
	}
	return builtins
}

// List prints one line per builtin, for the platform's own ":help" to report.
func (k *Kong) List(w io.Writer) {
	for _, node := range k.nodes {
		fmt.Fprintf(w, "  %-34s %s\n", node.Summary(), node.Help)
	}
}

// builtin is one command of the grammar.
type builtin struct {
	k    *Kong
	name string
}

func (b builtin) Restarts(args []string) bool {
	return b.k.cfg.Restart != nil && len(args) > 0 && b.k.cfg.Restart(args)
}

func (b builtin) Run(ctx context.Context, streams shell.Streams, args []string) (int, error) {
	parser, err := kong.New(b.k.cfg.Commands(), b.k.cfg.options(streams.Out, streams.Err)...)
	if err != nil {
		return 0, err
	}

	line := []string{shell.BuiltinMarker + b.name}
	if len(args) > 1 {
		line = append(line, args[1:]...)
	}
	kctx, err := parser.Parse(line)
	if err != nil {
		var parseErr *kong.ParseError
		if errors.As(err, &parseErr) && helpAsked(parseErr.Context) {
			return 0, nil
		}
		return 1, err
	}
	if helpAsked(kctx) {
		return 0, nil
	}

	kctx.BindTo(ctx, (*context.Context)(nil))
	if b.k.cfg.Bind != nil {
		kctx.Bind(b.k.cfg.Bind(ctx, streams)...)
	}

	if err := kctx.Run(); err != nil {
		return 1, err
	}
	return 0, nil
}

func helpAsked(kctx *kong.Context) bool {
	for _, flag := range kctx.Flags() {
		if flag.Name == "help" {
			return flag.Set
		}
	}
	return false
}
