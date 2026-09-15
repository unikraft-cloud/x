// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"regexp"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

func knownBuiltin(name string) bool { return name == "jobs" || name == "start" }

func TestHighlight(t *testing.T) {
	for _, tt := range []struct {
		name     string
		line     string
		style    lipgloss.Style
		coloured []string
		plain    []string
	}{
		{"empty", "", lipgloss.Style{}, nil, nil},
		{"plain-command", "ls -la", lipgloss.Style{}, nil, nil},
		{"single-quoted", "echo 'a string'", highlightStringStyle, []string{"'a string'"}, []string{"echo"}},
		{"double-quoted", `echo "a string"`, highlightStringStyle, []string{`"a string"`}, []string{"echo"}},
		{"unterminated-quote", "echo 'half", highlightStringStyle, []string{"'half"}, []string{"echo"}},
		{"pipe-and-redirect", "a | b > c", highlightSpecialStyle, []string{"|", ">"}, []string{"a", "b", "c"}},
		{"and-or", "a && b || c", highlightSpecialStyle, []string{"&", "|"}, []string{"a", "b", "c"}},
		{"dollar", "echo $HOME", highlightSpecialStyle, []string{"$"}, []string{"HOME"}},
		{"builtin", ":jobs", highlightBuiltinStyle, []string{":jobs"}, nil},
		{"builtin-with-arguments", ":start now", highlightBuiltinStyle, []string{":start"}, []string{"now"}},
		{"builtin-after-spaces", "   :jobs", highlightBuiltinStyle, []string{":jobs"}, nil},
		{"builtin-after-a-tab", "\t:jobs", highlightBuiltinStyle, []string{":jobs"}, nil},
		{"builtin-after-a-separator", "a; :jobs", highlightBuiltinStyle, []string{":jobs"}, []string{"a"}},
		{"builtin-after-a-pipe", "a | :jobs", highlightBuiltinStyle, []string{":jobs"}, []string{"a"}},
		{"builtin-on-the-next-line", "a\n:jobs", highlightBuiltinStyle, []string{":jobs"}, []string{"a"}},
		{"builtin-as-an-argument-is-a-word", "echo :jobs", lipgloss.Style{}, nil, nil},
		{"specials-inside-a-quote-stay-plain", "echo '| & ;'", highlightStringStyle, []string{"'| & ;'"}, nil},
		{"escaped-quote-inside-double-quotes", `echo "a\"b" tail`, highlightStringStyle, []string{`"a\"b"`}, []string{"tail"}},
		{"no-escapes-inside-single-quotes", `echo 'a\' b`, highlightStringStyle, []string{`'a\'`}, []string{"b"}},
		{"escaped-quote-outside-quotes", `echo \"hi there`, lipgloss.Style{}, nil, nil},
		{"escaped-special", `echo \| x`, lipgloss.Style{}, nil, nil},
		{"escaped-space-keeps-the-word", `echo foo\ :jobs`, lipgloss.Style{}, nil, nil},
		{"newline-inside-a-quote", "echo \"aaaaaaaaaa\nb\" tail", highlightStringStyle, []string{"\"aaaaaaaaaa", "b\""}, []string{"tail"}},
		{"tab-inside-a-quote", "echo \"a\tb\"", highlightStringStyle, []string{"\"a\tb\""}, []string{"echo"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := highlight(tt.line, knownBuiltin)

			assert.Equal(t, tt.line, ansi.Strip(got), "the text must survive unchanged")
			if len(tt.coloured) == 0 {
				assert.Equal(t, tt.line, got, "nothing to colour, so nothing added")
				return
			}
			for _, want := range tt.coloured {
				assert.Contains(t, got, tt.style.Render(want), "%q is not coloured as expected, in %q", want, got)
			}
			for _, want := range tt.plain {
				assert.NotContains(t, styledSpans(got), want, "%q is coloured, in %q", want, got)
			}
		})
	}
}

var sgrSpan = regexp.MustCompile(`(?s)\x1b\[[0-9;]*m(.*?)\x1b\[m`)

// styledSpans is every stretch of text that highlight wrapped in a style.
func styledSpans(got string) []string {
	var spans []string
	for _, m := range sgrSpan.FindAllStringSubmatch(got, -1) {
		spans = append(spans, m[1])
	}
	return spans
}
