// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package script

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLexLine_Basic(t *testing.T) {
	toks, err := lexLine("wait contains(\"foo\") and not contains(\"bar\")")
	require.NoError(t, err)

	got := flatTokens(toks)
	want := []string{"wait", "contains", "(", "\"foo\"", ")", "and", "not", "contains", "(", "\"bar\"", ")", "<eof>"}
	require.Equal(t, want, got)
}

func TestLexLine_Comment(t *testing.T) {
	toks, err := lexLine("snapshot # hello")
	require.NoError(t, err)
	require.Equal(t, []string{"snapshot", "<eof>"}, flatTokens(toks))

	toks, err = lexLine("# hello")
	require.NoError(t, err)
	require.Equal(t, []string{"<eof>"}, flatTokens(toks))
}

func TestLexLine_StringEscapes(t *testing.T) {
	toks, err := lexLine("contains(\"a\\\"b\")")
	require.NoError(t, err)
	require.Equal(t, []string{"contains", "(", "\"a\\\"b\"", ")", "<eof>"}, flatTokens(toks))
}

func TestLexLine_UnterminatedString(t *testing.T) {
	_, err := lexLine("wait contains(\"foo)")
	require.Error(t, err)
}

func flatTokens(toks []token) []string {
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		switch t.typ {
		case tokWord:
			out = append(out, t.lit)
		case tokString:
			out = append(out, strconv.Quote(t.lit))
		case tokLParen:
			out = append(out, "(")
		case tokRParen:
			out = append(out, ")")
		case tokEOF:
			out = append(out, "<eof>")
		default:
			out = append(out, "<unknown>")
		}
	}
	return out
}
