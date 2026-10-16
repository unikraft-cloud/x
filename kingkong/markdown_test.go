// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package kingkong

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// render renders text and strips the styling, so that a test compares layout
// alone.
func render(text string, width int) string {
	return ansi.Strip(RenderMarkdown(text, width))
}

func TestRenderMarkdownBlocks(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		width  int
		want   string
	}{
		{
			name:   "paragraph reflows to the width",
			source: "one two\nthree four\n\nfive six",
			width:  10,
			want:   "one two\nthree four\n\nfive six",
		},
		{
			name:   "heading indents the blocks below it",
			source: "Intro.\n\n## Section\n\nBody.",
			width:  40,
			want:   "Intro.\n\nSection:\n\n  Body.",
		},
		{
			name:   "nested heading indents further",
			source: "## Section\n\nBody.\n\n### Nested\n\nMore.",
			width:  40,
			want:   "Section:\n\n  Body.\n\n  Nested:\n\n    More.",
		},
		{
			name:   "code block keeps its own layout",
			source: "## Section\n\n```\na    b\n  c\n```",
			width:  10,
			want:   "Section:\n\n  a    b\n    c",
		},
		{
			name:   "a long heading wraps like any other text",
			source: "## A heading too long for the width",
			width:  20,
			want:   "A heading too long\nfor the width:",
		},
		{
			name:   "code block is not wrapped",
			source: "```\nnine chars and many more\n```",
			width:  10,
			want:   "nine chars and many more",
		},
		{
			name:   "bullet list aligns continuation lines",
			source: "- alpha beta gamma\n- delta",
			width:  12,
			want:   "- alpha beta\n  gamma\n- delta",
		},
		{
			name:   "ordered list markers are padded to a common width",
			source: "9. nine\n10. ten\n11. eleven",
			width:  40,
			want:   "9.  nine\n10. ten\n11. eleven",
		},
		{
			name:   "quote is indented",
			source: "> quoted",
			width:  40,
			want:   "  quoted",
		},
		{
			name:   "a heading in a quote starts at the quote indent",
			source: "> ## Section\n>\n> Body.",
			width:  40,
			want:   "  Section:\n\n    Body.",
		},
		{
			name:   "a nested list stays attached to its item",
			source: "- alpha\n  - beta\n- gamma",
			width:  40,
			want:   "- alpha\n  - beta\n- gamma",
		},
		{
			name:   "a link reference definition takes no space",
			source: "Para one.\n\n[ref]: http://example.com\n\nPara two.",
			width:  40,
			want:   "Para one.\n\nPara two.",
		},
		{
			name:   "several definitions together take no space",
			source: "A.\n\n[a]: http://a\n[b]: http://b\n\nB.",
			width:  40,
			want:   "A.\n\nB.",
		},
		{
			name:   "a reference link resolves to its destination",
			source: "See [the docs][ref].\n\n[ref]: http://example.com",
			width:  60,
			want:   "See the docs (http://example.com).",
		},
		{
			name:   "an html block keeps its closing line",
			source: "<!-- one\ntwo -->\n\nAfter.",
			width:  40,
			want:   "<!-- one\ntwo -->\n\nAfter.",
		},
		{
			name:   "plain prose is unchanged",
			source: "A single sentence.",
			width:  40,
			want:   "A single sentence.",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, render(test.source, test.width))
		})
	}
}

// Escapes and aligned columns are the reason code blocks exist in help text:
// a reflow would eat both.
func TestRenderMarkdownPreservesEscapes(t *testing.T) {
	source := "```\nkey\\[sub\\]=value    Literal bracket in key name.\n```"
	require.Contains(t, render(source, 80), `key\[sub\]=value    Literal bracket in key name.`)
}

func TestRenderMarkdownInline(t *testing.T) {
	rendered := RenderMarkdown("Use `--env` for the whole **environment**.", 80)

	// The markers are consumed by the styling rather than printed.
	require.Equal(t, "Use --env for the whole environment.", ansi.Strip(rendered))
	require.Contains(t, rendered, CodeColor("--env"))
	require.Contains(t, rendered, Bold("environment"))
}

func TestRenderMarkdownLink(t *testing.T) {
	rendered := render("See [the docs](https://unikraft.com/docs/cli).", 80)
	require.Equal(t, "See the docs (https://unikraft.com/docs/cli).", rendered)
}

// A help text with no headings at all must not gain an indent.
func TestRenderMarkdownWithoutHeadings(t *testing.T) {
	rendered := render("First.\n\nSecond.", 80)
	for line := range strings.SplitSeq(rendered, "\n") {
		require.False(t, strings.HasPrefix(line, " "), "unexpected indent: %q", line)
	}
}

// A style which wraps a span that is already styled must not print the inner
// escape sequences as visible characters.
func TestRenderMarkdownNestedStyles(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		want   string
	}{
		{"code span in a heading", "## Use `--foo` now", "Use --foo now:"},
		{"bold in a heading", "## Heading with **bold**", "Heading with bold:"},
		{"bold in a link label", "[**bold** label](http://x)", "bold label (http://x)"},
		{"code span in bold", "**bold with `code` inside**", "bold with code inside"},
		{"code span in italic", "*italic with `code` inside*", "italic with code inside"},
		{"italic in bold", "**bold with *italic* inside**", "bold with italic inside"},
		{"link in bold", "**bold with [a link](http://x) inside**", "bold with a link (http://x) inside"},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, render(test.source, 80))
		})
	}
}

func TestRenderMarkdownResolvesEscapes(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		want   string
	}{
		{"backslash escape in prose", `A literal star as a\*b`, "A literal star as a*b"},
		{"named entity", "AT&amp;T", "AT&T"},
		{"numeric entity", "&#38;", "&"},
		{"code span is literal", "Type `a\\*b` exactly", `Type a\*b exactly`},
		{"code block is literal", "```\nkey\\[sub\\]=value\n```", `key\[sub\]=value`},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, render(test.source, 80))
		})
	}
}

// CommonMark turns a line ending inside a code span into a space.
func TestRenderMarkdownCodeSpanLineEnding(t *testing.T) {
	require.Equal(t, "Use foo bar here.", render("Use `foo\nbar` here.", 80))
}

func TestRenderMarkdownImage(t *testing.T) {
	require.Equal(t, "alt text (img.png) here", render("![alt text](img.png) here", 80))
}

// A width the writer cannot use leaves the text alone rather than breaking it
// one character to a line.
func TestRenderMarkdownUnusableWidth(t *testing.T) {
	for _, width := range []int{0, -1} {
		require.Equal(t, "hello world", render("hello world", width))
	}
}
