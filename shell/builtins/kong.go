// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package builtins answers a session's ":" lines from a kong grammar
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
	"unikraft.com/x/stdio"
)

type KongConfig struct {
	Commands func() any
	Options  []kong.Option
	Bind     func(ctx context.Context, streams stdio.Stdio) []any
	Restart  func(args []string) bool
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

	// The session routes on the sigil and this package puts it back on the line
	for _, node := range nodes {
		if node.Type != kong.CommandNode {
			return nil, fmt.Errorf("%q is not a command: a grammar's top level is its commands", node.Name)
		}
		for _, name := range append([]string{node.Name}, node.Aliases...) {
			if !strings.HasPrefix(name, shell.BuiltinMarker) {
				return nil, fmt.Errorf("command %q is not named with the %q sigil the session routes on", name, shell.BuiltinMarker)
			}
		}
	}

	return &Kong{cfg: cfg, nodes: nodes}, nil
}

// errHelpAsked ends a parse the moment help has been printed
var errHelpAsked = errors.New("the line asked for help")

func (c KongConfig) options(out, err io.Writer) []kong.Option {
	return append([]kong.Option{
		kong.Name(""),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true, FlagsLast: true}),
		kong.Writers(out, err),
		kong.Help(func(options kong.HelpOptions, kctx *kong.Context) error {
			if err := kong.DefaultHelpPrinter(options, kctx); err != nil {
				return err
			}
			return errHelpAsked
		}),
	}, c.Options...)
}

// Builtins is one [shell.Builtin] per command of the grammar
func (k *Kong) Builtins() map[string]shell.Builtin {
	builtins := make(map[string]shell.Builtin, len(k.nodes))
	for _, node := range k.nodes {
		name := strings.TrimPrefix(node.Name, shell.BuiltinMarker)
		for _, routed := range append([]string{node.Name}, node.Aliases...) {
			builtins[strings.TrimPrefix(routed, shell.BuiltinMarker)] = builtin{k: k, name: name}
		}
	}
	return builtins
}

// List prints one line per builtin
func (k *Kong) List(w io.Writer) {
	for _, node := range k.nodes {
		if node.Hidden {
			continue
		}
		fmt.Fprintf(w, "  %-34s %s\n", node.Summary(), node.Help)
	}
}

// builtin is one command of the grammar.
type builtin struct {
	k    *Kong
	name string
}

func (b builtin) Restarts(args []string) bool {
	if b.k.cfg.Restart == nil || len(args) == 0 {
		return false
	}
	if _, err := b.parse(io.Discard, io.Discard, args); errors.Is(err, errHelpAsked) {
		return false
	}
	return b.k.cfg.Restart(args)
}

// parse reads the line the way the session hands it over: the name it routed
// on, which the grammar spells with the sigil, and the arguments after it.
func (b builtin) parse(out, err io.Writer, args []string) (*kong.Context, error) {
	parser, perr := kong.New(b.k.cfg.Commands(), b.k.cfg.options(out, err)...)
	if perr != nil {
		return nil, perr
	}
	return parser.Parse(append([]string{shell.BuiltinMarker + b.name}, args[1:]...))
}

func (b builtin) Run(ctx context.Context, streams stdio.Stdio, args []string) (int, error) {
	kctx, err := b.parse(streams.Stdout, streams.Stderr, args)
	switch {
	case errors.Is(err, errHelpAsked):
		return 0, nil
	case err != nil:
		return 1, err
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
