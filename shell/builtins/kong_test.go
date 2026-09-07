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
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/shell"
	"unikraft.com/x/shell/builtins"
)

// session is what a platform's builtins run against: its own stdio, and
// whatever state the commands need.
type session struct {
	out  io.Writer
	list func(io.Writer)
	ran  []string
}

type commands struct {
	Edit  editCmd  `cmd:"" name:":edit" help:"Change this instance's settings."`
	Get   getCmd   `cmd:"" name:":get" help:"Inspect this instance."`
	Help  helpCmd  `cmd:"" name:":help" help:"List these builtins."`
	Mount mountCmd `cmd:"" name:":mount" help:"Attach a volume to this instance."`
	Stop  stopCmd  `cmd:"" name:":stop" help:"Stop this instance."`
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
	Output string `name:"output" help:"Output format." placeholder:"format"`
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
	fmt.Fprintln(s.out, "Builtins run on this CLI rather than the instance:")
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

type stopCmd struct{}

func (stopCmd) Run(s *session) error {
	s.ran = append(s.ran, "stop")
	return nil
}

func newKong(t *testing.T, s *session, restart func([]string) bool) *builtins.Kong {
	t.Helper()

	k, err := builtins.NewKong(builtins.KongConfig{
		Commands: func() any { return &commands{} },
		Options: []kong.Option{
			kong.Description("Builtins run on this CLI rather than the instance."),
			kong.Vars{"format": "table"},
		},
		Bind: func(_ context.Context, streams shell.Streams) []any {
			s.out = streams.Out
			return []any{s}
		},
		Restart: restart,
	})
	require.NoError(t, err)

	s.list = k.List
	return k
}

func TestNames(t *testing.T) {
	k := newKong(t, &session{}, nil)

	assert.Equal(t, []string{"edit", "get", "help", "mount", "stop"}, k.Names(),
		"sorted, so completion and help are stable")
}

func TestAGrammarIsRequired(t *testing.T) {
	_, err := builtins.NewKong(builtins.KongConfig{})
	assert.Error(t, err)
}

func TestABuiltinReachesWhatItWasBoundTo(t *testing.T) {
	s := &session{}
	k := newKong(t, s, nil)

	var out bytes.Buffer
	code, err := k.Run(t.Context(), shell.Streams{Out: &out, Err: &out},
		[]string{"mount", "data", "/srv", "--readonly"})

	require.NoError(t, err)
	assert.Zero(t, code)
	assert.Equal(t, []string{"mount=data:/srv"}, s.ran)
	assert.Empty(t, out.String(), "a builtin that worked says nothing extra")
}

func TestTheContextAndTheVariablesReachTheBuiltin(t *testing.T) {
	s := &session{}
	k := newKong(t, s, nil)

	code, err := k.Run(t.Context(), shell.Streams{Out: io.Discard, Err: io.Discard}, []string{"get"})

	require.NoError(t, err)
	assert.Zero(t, code)
	assert.Equal(t, []string{"get="}, s.ran, "the platform's own kong.Vars are honoured")
}

func TestReportWhatIsWrongWithALine(t *testing.T) {
	for _, tt := range []struct {
		name string
		line []string
		want string
		code int
	}{
		// An unknown builtin is not the builtin's failure, so it reports no
		// status of its own and the session answers with one.
		{"unknown-builtin", []string{"nonsense"}, `unknown builtin "nonsense"; try ":help"`, 0},
		{"missing-arguments", []string{"mount"}, "<volume>", 1},
		{"missing-one-argument", []string{"mount", "vol"}, "<path>", 1},
		{"unknown-flag", []string{"get", "--nonsense"}, "unknown flag --nonsense", 1},
		{"not-a-field", []string{"edit", "nonsense"}, "is not <field>=<value>", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer

			code, err := newKong(t, &session{}, nil).Run(t.Context(),
				shell.Streams{Out: &out, Err: &out}, tt.line)

			require.Error(t, err)
			assert.Equal(t, tt.code, code)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestABuiltinAnswersItsOwnHelp(t *testing.T) {
	var out bytes.Buffer

	code, err := newKong(t, &session{}, nil).Run(t.Context(),
		shell.Streams{Out: &out, Err: &out}, []string{"mount", "--help"})

	require.NoError(t, err)
	assert.Zero(t, code)
	assert.Contains(t, out.String(), "<volume>")
	assert.Contains(t, out.String(), "--readonly")
}

func TestTheListingIsTheGrammar(t *testing.T) {
	s := &session{}
	k := newKong(t, s, nil)

	var out bytes.Buffer
	code, err := k.Run(t.Context(), shell.Streams{Out: &out}, []string{"help"})

	require.NoError(t, err)
	assert.Zero(t, code)

	printed := out.String()
	assert.Contains(t, printed, ":mount <volume> <path>")
	assert.Contains(t, printed, ":edit <field=value>")
	assert.Contains(t, printed, "Attach a volume to this instance.")
	for _, name := range k.Names() {
		assert.Contains(t, printed, shell.BuiltinMarker+name)
	}
	assert.NotContains(t, printed, "runs on the instance",
		"what the session adds is the session's to add")
}

func TestWhichBuiltinsRestart(t *testing.T) {
	k := newKong(t, &session{}, func(args []string) bool { return args[0] == "stop" })

	assert.True(t, k.Restarts([]string{"stop"}))
	assert.False(t, k.Restarts([]string{"get"}))
	assert.False(t, k.Restarts(nil))

	var _ shell.Restarts = k
	var _ shell.Builtins = k
}

func TestNothingRestartsWithoutOne(t *testing.T) {
	k := newKong(t, &session{}, nil)

	for _, name := range k.Names() {
		assert.False(t, k.Restarts([]string{name}), "%s leaves the session nothing to wait for", name)
	}
}
