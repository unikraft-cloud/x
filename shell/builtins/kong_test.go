// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package builtins_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/shell"
	"unikraft.com/x/shell/builtins"
	"unikraft.com/x/stdio"
)

// restarts is what the session asks a builtin before waiting on the instance
type restarts interface{ Restarts(args []string) bool }

// session is what a platform's builtins run against
type session struct {
	out   io.Writer
	list  func(io.Writer)
	ran   []string
	hooks []string
}

type commands struct {
	Edit   editCmd   `cmd:"" name:":edit" help:"Change this instance's settings."`
	Get    getCmd    `cmd:"" name:":get" help:"Inspect this instance."`
	Help   helpCmd   `cmd:"" name:":help" help:"List these builtins."`
	Mount  mountCmd  `cmd:"" name:":mount" help:"Attach a volume to this instance."`
	Secret secretCmd `cmd:"" name:":secret" hidden:"" help:"Not for the listing."`
	Stop   stopCmd   `cmd:"" name:":stop" aliases:":halt" help:"Stop this instance."`
}

type editCmd struct {
	Fields []string `arg:"" name:"field=value" help:"Fields to set on this instance."`
}

func (c editCmd) Run(s *session) error {
	for _, field := range c.Fields {
		if _, _, ok := strings.Cut(field, "="); !ok {
			return fmt.Errorf("%q is not <field>=<value>", field)
		}
	}
	s.ran = append(s.ran, "edit")
	return nil
}

type getCmd struct {
	Output string `name:"output" help:"Output format." default:"${format}" placeholder:"${format}"`
}

func (c getCmd) Run(ctx context.Context, s *session) error {
	if ctx == nil {
		return errors.New("no context reached the builtin")
	}
	s.ran = append(s.ran, "get="+c.Output)
	return nil
}

type helpCmd struct{}

func (helpCmd) Run(s *session) error {
	fmt.Fprintln(s.out, "Builtins run on this CLI, not on the instance:")
	s.list(s.out)
	return nil
}

type mountCmd struct {
	Volume   string `arg:"" help:"Volume to attach."`
	At       string `arg:"" name:"path" help:"Absolute mount path inside the instance."`
	Readonly bool   `help:"Mount the volume as read-only."`
}

func (c mountCmd) Run(s *session) error {
	s.ran = append(s.ran, "mount="+c.Volume+":"+c.At)
	return nil
}

// stopCmd carries a hook, which a line that only asks for help must not reach.
// It records through a pointer of its own, as what the platform binds does not
// reach a hook: the adapter binds after the line is parsed.
type stopCmd struct{ hooked *[]string }

func (c stopCmd) BeforeApply() error {
	if c.hooked != nil {
		*c.hooked = append(*c.hooked, "stop-hook")
	}
	return nil
}

func (stopCmd) Run(s *session) error {
	s.ran = append(s.ran, "stop")
	return nil
}

type secretCmd struct{}

func (secretCmd) Run(s *session) error {
	s.ran = append(s.ran, "secret")
	return nil
}

func newKong(t *testing.T, s *session, restart func([]string) bool) *builtins.Kong {
	t.Helper()

	k, err := builtins.NewKong(builtins.KongConfig{
		Commands: func() any { return &commands{Stop: stopCmd{hooked: &s.hooks}} },
		Options: []kong.Option{
			kong.Description("Builtins run on this CLI, not on the instance."),
			kong.Vars{"format": "table"},
		},
		Bind: func(_ context.Context, streams stdio.Stdio) []any {
			s.out = streams.Stdout
			return []any{s}
		},
		Restart: restart,
	})
	require.NoError(t, err)

	s.list = k.List
	return k
}

// run runs the builtin the line names, as the session would.
func run(t *testing.T, k *builtins.Kong, streams stdio.Stdio, line ...string) (int, error) {
	t.Helper()

	b, ok := k.Builtins()[line[0]]
	require.True(t, ok, "no builtin %q", line[0])
	return b.Run(t.Context(), streams, line)
}

func TestTheGrammarIsTheBuiltins(t *testing.T) {
	k := newKong(t, &session{}, nil)

	assert.Equal(t, []string{"edit", "get", "halt", "help", "mount", "secret", "stop"}, slices.Sorted(maps.Keys(k.Builtins())))
}

func TestAGrammarIsRequired(t *testing.T) {
	_, err := builtins.NewKong(builtins.KongConfig{})
	assert.Error(t, err)
}

func TestAGrammarNamesItsCommandsWithTheSigil(t *testing.T) {
	type bare struct {
		Stop stopCmd `cmd:"" help:"Stop this instance."`
	}

	_, err := builtins.NewKong(builtins.KongConfig{Commands: func() any { return &bare{} }})

	require.ErrorContains(t, err, `"stop"`, "built, listed and completed, it would fail every invocation instead")
	assert.ErrorContains(t, err, shell.BuiltinMarker)
}

func TestAGrammarIsCommandsOnly(t *testing.T) {
	type branching struct {
		Target struct {
			Name string  `arg:"" name:"target"`
			Stop stopCmd `cmd:"" name:":stop" help:"Stop this instance."`
		} `arg:"" name:"target" help:"An instance."`
	}

	_, err := builtins.NewKong(builtins.KongConfig{Commands: func() any { return &branching{} }})

	require.ErrorContains(t, err, `"target"`)
	assert.ErrorContains(t, err, "not a command")
}

func TestABuiltinReachesWhatItWasBoundTo(t *testing.T) {
	s := &session{}
	k := newKong(t, s, nil)

	var out bytes.Buffer
	code, err := run(t, k, stdio.Stdio{Stdout: &out, Stderr: &out}, "mount", "data", "/srv", "--readonly")

	require.NoError(t, err)
	assert.Zero(t, code)
	assert.Equal(t, []string{"mount=data:/srv"}, s.ran)
	assert.Empty(t, out.String(), "a builtin that worked says nothing extra")
}

func TestTheContextAndTheVariablesReachTheBuiltin(t *testing.T) {
	s := &session{}
	k := newKong(t, s, nil)

	code, err := run(t, k, stdio.Stdio{Stdout: io.Discard, Stderr: io.Discard}, "get")

	require.NoError(t, err)
	assert.Zero(t, code)
	assert.Equal(t, []string{"get=table"}, s.ran, "the platform's own kong.Vars are honoured")
}

func TestReportWhatIsWrongWithALine(t *testing.T) {
	for _, tt := range []struct {
		name string
		line []string
		want string
		code int
	}{
		{"missing-arguments", []string{"mount"}, "<volume>", 1},
		{"missing-one-argument", []string{"mount", "vol"}, "<path>", 1},
		{"unknown-flag", []string{"get", "--nonsense"}, "unknown flag --nonsense", 1},
		{"not-a-field", []string{"edit", "nonsense"}, "is not <field>=<value>", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer

			code, err := run(t, newKong(t, &session{}, nil), stdio.Stdio{Stdout: &out, Stderr: &out}, tt.line...)

			require.Error(t, err)
			assert.Equal(t, tt.code, code)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestABuiltinAnswersItsOwnHelp(t *testing.T) {
	t.Run("in-place-of-the-arguments-it-lacks", func(t *testing.T) {
		var out bytes.Buffer

		code, err := run(t, newKong(t, &session{}, nil), stdio.Stdio{Stdout: &out, Stderr: &out}, "mount", "--help")

		require.NoError(t, err)
		assert.Zero(t, code)
		assert.Contains(t, out.String(), "<volume>")
		assert.Contains(t, out.String(), "--readonly")
	})

	t.Run("in-place-of-running", func(t *testing.T) {
		s := &session{}
		var out bytes.Buffer

		code, err := run(t, newKong(t, s, nil), stdio.Stdio{Stdout: &out, Stderr: &out}, "stop", "--help")

		require.NoError(t, err)
		assert.Zero(t, code)
		assert.Contains(t, out.String(), "Stop this instance.")
		assert.Empty(t, s.ran, "a line that parses whole still only asked for help")
	})

	t.Run("in-place-of-the-hooks-running-too", func(t *testing.T) {
		s := &session{}

		code, err := run(t, newKong(t, s, nil), stdio.Stdio{Stdout: io.Discard, Stderr: io.Discard}, "stop", "--help")

		require.NoError(t, err)
		assert.Zero(t, code)
		assert.Empty(t, s.hooks, "help asked before the command's hooks could run")
	})

	t.Run("without-restarting-anything", func(t *testing.T) {
		k := newKong(t, &session{}, func(args []string) bool { return args[0] == "stop" }).Builtins()

		assert.False(t, k["stop"].(restarts).Restarts([]string{"stop", "--help"}),
			"a line that only asked for help left the instance alone")
	})
}

func TestTheListingIsTheGrammar(t *testing.T) {
	s := &session{}
	k := newKong(t, s, nil)

	var out bytes.Buffer
	code, err := run(t, k, stdio.Stdio{Stdout: &out}, "help")

	require.NoError(t, err)
	assert.Zero(t, code)

	printed := out.String()
	assert.Contains(t, printed, ":mount <volume> <path>")
	assert.Contains(t, printed, ":edit <field=value>")
	assert.Contains(t, printed, "Attach a volume to this instance.")
	for _, name := range []string{"edit", "get", "help", "mount", "stop"} {
		assert.Contains(t, printed, shell.BuiltinMarker+name)
	}
	assert.NotContains(t, printed, "runs on the instance",
		"what the session adds is the session's to add")
}

func TestAnAliasIsABuiltinToo(t *testing.T) {
	s := &session{}
	k := newKong(t, s, nil)

	names := k.Builtins()
	require.Contains(t, names, "halt", "kong parses and advertises the alias, so the session must route it")

	code, err := run(t, k, stdio.Stdio{Stdout: io.Discard, Stderr: io.Discard}, "halt")

	require.NoError(t, err)
	assert.Zero(t, code)
	assert.Equal(t, []string{"stop"}, s.ran, "the alias runs what it is an alias for")
	assert.Equal(t, []string{"stop-hook"}, s.hooks, "hooks and all")
}

func TestAHiddenCommandIsNotListed(t *testing.T) {
	s := &session{}
	k := newKong(t, s, nil)

	var out bytes.Buffer
	k.List(&out)
	assert.NotContains(t, out.String(), ":secret", "a hidden command is hidden from the listing")

	require.Contains(t, k.Builtins(), "secret", "but still answers when it is named")
	code, err := run(t, k, stdio.Stdio{Stdout: io.Discard, Stderr: io.Discard}, "secret")
	require.NoError(t, err)
	assert.Zero(t, code)
}

func TestWhichBuiltinsRestart(t *testing.T) {
	k := newKong(t, &session{}, func(args []string) bool { return args[0] == "stop" }).Builtins()

	stop, ok := k["stop"].(restarts)
	require.True(t, ok, "every builtin of the grammar answers about restarting")
	assert.True(t, stop.Restarts([]string{"stop"}))
	assert.False(t, stop.Restarts(nil))
	assert.False(t, k["get"].(restarts).Restarts([]string{"get"}))
}

func TestNothingRestartsWithoutOne(t *testing.T) {
	for name, b := range newKong(t, &session{}, nil).Builtins() {
		assert.False(t, b.(restarts).Restarts([]string{name}), "%s leaves the session nothing to wait for", name)
	}
}
