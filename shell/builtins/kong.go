// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package builtins answers a session's ":" lines from a kong grammar, so that a
// platform declares its builtins as commands rather than implementing
// [shell.Builtins] itself.
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

	// Restart reports a builtin that takes the instance down and up again. A
	// platform without one leaves this nil.
	Restart func(args []string) bool
}

// Kong answers as [shell.Builtins], and as [shell.Restarts] for a platform that
// gave it a Restart.
type Kong struct {
	cfg   KongConfig
	nodes []*kong.Node
	help  string
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

	k := &Kong{cfg: cfg, nodes: nodes}
	if help := shell.BuiltinMarker + "help"; slices.ContainsFunc(nodes, func(n *kong.Node) bool {
		return n.Name == help
	}) {
		k.help = help
	}
	return k, nil
}

func (c KongConfig) options(out, err io.Writer) []kong.Option {
	return append([]kong.Option{
		kong.Name(""),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true, FlagsLast: true}),
		kong.Writers(out, err),
		kong.Exit(func(int) {}),
	}, c.Options...)
}

func (k *Kong) Names() []string {
	names := make([]string, 0, len(k.nodes))
	for _, node := range k.nodes {
		names = append(names, strings.TrimPrefix(node.Name, shell.BuiltinMarker))
	}
	return names
}

// List prints one line per builtin, for the platform's own ":help" to report.
func (k *Kong) List(w io.Writer) {
	for _, node := range k.nodes {
		fmt.Fprintf(w, "  %-34s %s\n", node.Summary(), node.Help)
	}
}

func (k *Kong) Restarts(args []string) bool {
	return k.cfg.Restart != nil && len(args) > 0 && k.cfg.Restart(args)
}

func (k *Kong) Run(ctx context.Context, streams shell.Streams, args []string) (int, error) {
	if len(args) == 0 {
		return 0, errors.New("no builtin to run")
	}
	if !slices.Contains(k.Names(), args[0]) {
		return 0, k.unknown(args[0])
	}

	parser, err := kong.New(k.cfg.Commands(), k.cfg.options(streams.Out, streams.Err)...)
	if err != nil {
		return 0, err
	}

	kctx, err := parser.Parse(append([]string{shell.BuiltinMarker + args[0]}, args[1:]...))
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
	if k.cfg.Bind != nil {
		kctx.Bind(k.cfg.Bind(ctx, streams)...)
	}

	if err := kctx.Run(); err != nil {
		return 1, err
	}
	return 0, nil
}

func (k *Kong) unknown(name string) error {
	if k.help == "" {
		return fmt.Errorf("unknown builtin %q", name)
	}
	return fmt.Errorf("unknown builtin %q; try %q", name, k.help)
}

func helpAsked(kctx *kong.Context) bool {
	for _, flag := range kctx.Flags() {
		if flag.Name == "help" {
			return flag.Set
		}
	}
	return false
}
