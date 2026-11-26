// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package script

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

func TestParseLine_Comment(t *testing.T) {
	_, skip, err := parseLine("# hello")
	require.NoError(t, err)
	require.True(t, skip)
}

func TestParseLine_Snapshot(t *testing.T) {
	cmd, skip, err := parseLine("snapshot")
	require.NoError(t, err)
	require.False(t, skip)
	require.Equal(t, CommandSnapshot, cmd.Kind)
}

func TestParseLine_Sleep(t *testing.T) {
	cmd, skip, err := parseLine("sleep 150ms")
	require.NoError(t, err)
	require.False(t, skip)
	require.Equal(t, CommandSleep, cmd.Kind)
	require.Equal(t, 150*time.Millisecond, cmd.Sleep)
}

func TestParseLine_Key(t *testing.T) {
	cmd, skip, err := parseLine("key enter")
	require.NoError(t, err)
	require.False(t, skip)
	require.Equal(t, CommandKey, cmd.Kind)
	require.Equal(t, tea.KeyEnter, cmd.Key.Code)

	cmd, _, err = parseLine("key ctrl+c")
	require.NoError(t, err)
	require.Equal(t, rune('c'), cmd.Key.Code)
	require.True(t, cmd.Key.Mod.Contains(tea.ModCtrl))
	require.Empty(t, cmd.Key.Text)

	cmd, _, err = parseLine("key shift+tab")
	require.NoError(t, err)
	require.Equal(t, tea.KeyTab, cmd.Key.Code)
	require.True(t, cmd.Key.Mod.Contains(tea.ModShift))
}

func TestParseLine_WaitSimple(t *testing.T) {
	cmd, skip, err := parseLine("wait contains(\"foo\")")
	require.NoError(t, err)
	require.False(t, skip)
	require.Equal(t, CommandWait, cmd.Kind)

	ce, ok := cmd.Wait.(*ContainsExpr)
	require.True(t, ok)
	require.Equal(t, "foo", ce.Needle)
	require.True(t, cmd.Wait.Eval("xxfooxx"))
	require.False(t, cmd.Wait.Eval("bar"))
}

func TestParseLine_WaitNot(t *testing.T) {
	cmd, _, err := parseLine("wait not contains(\"foo\")")
	require.NoError(t, err)
	require.Equal(t, CommandWait, cmd.Kind)

	ne, ok := cmd.Wait.(*NotExpr)
	require.True(t, ok)
	_, ok = ne.X.(*ContainsExpr)
	require.True(t, ok)
	require.False(t, cmd.Wait.Eval("xxfooxx"))
	require.True(t, cmd.Wait.Eval("bar"))
}

func TestParseWaitExpr_BooleanOpsAndPrecedence(t *testing.T) {
	toks, err := lexLine("contains(\"foo\") or contains(\"bar\") and contains(\"baz\")")
	require.NoError(t, err)
	expr, err := parseWaitExpr(toks)
	require.NoError(t, err)

	or, ok := expr.(*OrExpr)
	require.True(t, ok)
	_, ok = or.Left.(*ContainsExpr)
	require.True(t, ok)

	and, ok := or.Right.(*AndExpr)
	require.True(t, ok)
	require.IsType(t, &ContainsExpr{}, and.Left)
	require.IsType(t, &ContainsExpr{}, and.Right)

	require.True(t, expr.Eval("foo"))
	require.True(t, expr.Eval("bar baz"))
	require.False(t, expr.Eval("bar"))
}

func TestParseWaitExpr_Parens(t *testing.T) {
	toks, err := lexLine("(not contains(\"foo\")) or (contains(\"bar\"))")
	require.NoError(t, err)
	expr, err := parseWaitExpr(toks)
	require.NoError(t, err)
	require.True(t, expr.Eval("bar"))
	require.False(t, expr.Eval("foo"))
}

func TestParseWaitExpr_StringEscapes(t *testing.T) {
	toks, err := lexLine("contains(\"a\\\"b\")")
	require.NoError(t, err)
	expr, err := parseWaitExpr(toks)
	require.NoError(t, err)
	ce := expr.(*ContainsExpr)
	require.Equal(t, "a\"b", ce.Needle)
}
