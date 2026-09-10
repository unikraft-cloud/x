// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package kingkong

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	gtext "github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// IndentWidth is the number of columns a nested block is indented by.
const IndentWidth = 2

// markdown parses the Markdown subset that help text is written in: headings,
// paragraphs, code blocks, lists, quotes and inline styling.
var markdown = goldmark.New()

// RenderMarkdown renders Markdown as styled terminal text wrapped to width.
// A heading becomes an underlined title, and a code block keeps its layout.
func RenderMarkdown(text string, width int) string {
	return strings.Join(renderMarkdown(text, width), "\n")
}

func renderMarkdown(text string, width int) []string {
	source := []byte(strings.ReplaceAll(text, "\r\n", "\n"))
	doc := markdown.Parser().Parse(gtext.NewReader(source))

	r := &markdownRenderer{source: source, width: width}
	r.blocks(doc, 0)
	return r.lines()
}

// markdownRenderer accumulates the rendered lines of a Markdown document.
type markdownRenderer struct {
	source []byte
	width  int
	out    []string
}

// topHeadingLevel returns the level of the shallowest heading among the
// children of parent, which is the level rendered flush with its container.
func topHeadingLevel(parent ast.Node) int {
	top := 0
	for node := parent.FirstChild(); node != nil; node = node.NextSibling() {
		heading, ok := node.(*ast.Heading)
		if ok && (top == 0 || heading.Level < top) {
			top = heading.Level
		}
	}
	return top
}

// blocks renders the block children of parent, starting at indent. A heading
// moves the indent of the blocks which follow it in the same parent.
func (r *markdownRenderer) blocks(parent ast.Node, indent int) {
	top := topHeadingLevel(parent)
	content := indent

	for node := parent.FirstChild(); node != nil; node = node.NextSibling() {
		r.separate(node)

		switch node := node.(type) {
		case *ast.Heading:
			title := indent + max(node.Level-top, 0)*IndentWidth
			content = title + IndentWidth
			r.wrap(title, Underline(unstyled(r.inline(node)))+":")

		case *ast.FencedCodeBlock, *ast.CodeBlock:
			r.preformatted(content, node)

		case *ast.HTMLBlock:
			r.preformatted(content, node)
			if node.HasClosure() {
				r.raw(content, node.ClosureLine.Value(r.source))
			}

		case *ast.List:
			r.list(node, content)

		case *ast.Blockquote:
			r.blocks(node, content+IndentWidth)

		case *ast.ThematicBreak:
			r.write(content, DimmedColor(strings.Repeat("-", max(r.width-content, 1))))

		case *ast.LinkReferenceDefinition:
			// A definition is metadata. goldmark writes nothing for it, and
			// its empty text would otherwise become a blank line.

		default:
			r.wrap(content, r.inline(node))
		}
	}
}

// separate puts a blank line before node. A block which follows the text of a
// tight list item stays attached to that text.
func (r *markdownRenderer) separate(node ast.Node) {
	if _, tight := node.PreviousSibling().(*ast.TextBlock); tight {
		return
	}
	r.blank()
}

// list renders an ordered or bullet list, aligning the continuation lines of
// an item with the text after its marker.
func (r *markdownRenderer) list(list *ast.List, indent int) {
	markers := listMarkers(list)

	for i, item := 0, list.FirstChild(); item != nil; i, item = i+1, item.NextSibling() {
		sub := &markdownRenderer{source: r.source, width: r.width}
		sub.blocks(item, indent+len(markers[i]))

		lines := sub.lines()
		if len(lines) == 0 {
			continue
		}

		// The marker takes the place of the indent the item was rendered at.
		lines[0] = strings.Repeat(" ", indent) + markers[i] +
			strings.TrimPrefix(lines[0], strings.Repeat(" ", indent+len(markers[i])))

		if i > 0 && !list.IsTight {
			r.blank()
		}
		r.out = append(r.out, lines...)
	}
}

// listMarkers returns the marker of every item in list, padded to a common
// width so that the items line up.
func listMarkers(list *ast.List) []string {
	markers := make([]string, 0, list.ChildCount())
	width := 0

	for i, item := 0, list.FirstChild(); item != nil; i, item = i+1, item.NextSibling() {
		marker := "-"
		if list.IsOrdered() {
			marker = fmt.Sprintf("%d.", list.Start+i)
		}
		width = max(width, len(marker))
		markers = append(markers, marker)
	}

	for i, marker := range markers {
		markers[i] = marker + strings.Repeat(" ", width-len(marker)+1)
	}
	return markers
}

// preformatted writes the raw lines of node at indent, without wrapping them.
func (r *markdownRenderer) preformatted(indent int, node ast.Node) {
	pad := strings.Repeat(" ", indent)
	segments := node.Lines()

	for i := range segments.Len() {
		segment := segments.At(i)
		r.out = append(r.out, strings.TrimRight(pad+string(segment.Value(r.source)), " \t\r\n"))
	}
}

// raw appends one unwrapped line of literal bytes at indent.
func (r *markdownRenderer) raw(indent int, line []byte) {
	pad := strings.Repeat(" ", indent)
	r.out = append(r.out, strings.TrimRight(pad+string(line), " \t\r\n"))
}

// wrap writes text at indent, wrapped to the remaining width. A hard line
// break in the source starts a new line.
func (r *markdownRenderer) wrap(indent int, text string) {
	pad := strings.Repeat(" ", indent)
	width := r.width - indent

	for paragraph := range strings.SplitSeq(text, "\n") {
		wrapped := ansi.Wrap(strings.TrimSpace(paragraph), width, "-")
		for line := range strings.SplitSeq(wrapped, "\n") {
			r.out = append(r.out, strings.TrimRight(pad+line, " "))
		}
	}
}

// write appends a single line at indent.
func (r *markdownRenderer) write(indent int, text string) {
	r.out = append(r.out, strings.TrimRight(strings.Repeat(" ", indent)+text, " "))
}

// blank separates the previous block from the next one.
func (r *markdownRenderer) blank() {
	if len(r.out) > 0 && r.out[len(r.out)-1] != "" {
		r.out = append(r.out, "")
	}
}

// lines returns the rendered lines without the blanks around them.
func (r *markdownRenderer) lines() []string {
	start, end := 0, len(r.out)
	for start < end && r.out[start] == "" {
		start++
	}
	for end > start && r.out[end-1] == "" {
		end--
	}
	return r.out[start:end]
}

// inline renders the inline children of node into a styled string.
func (r *markdownRenderer) inline(node ast.Node) string {
	var buf strings.Builder
	r.writeInline(&buf, node)
	return buf.String()
}

func (r *markdownRenderer) writeInline(buf *strings.Builder, node ast.Node) {
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		switch child := child.(type) {
		case *ast.Text:
			buf.WriteString(r.literal(child.Segment.Value(r.source), child.IsRaw()))
			switch {
			case child.HardLineBreak():
				buf.WriteString("\n")
			case child.SoftLineBreak():
				buf.WriteString(" ")
			}

		case *ast.String:
			buf.WriteString(r.literal(child.Value, child.IsRaw()))

		case *ast.CodeSpan:
			buf.WriteString(CodeColor(r.rawText(child)))

		case *ast.Emphasis:
			if child.Level > 1 {
				buf.WriteString(Bold(r.inline(child)))
			} else {
				buf.WriteString(Italic(r.inline(child)))
			}

		case *ast.Link:
			label := unstyled(r.inline(child))
			destination := string(child.Destination)
			buf.WriteString(Underline(label))
			if label != destination {
				buf.WriteString(DimmedColor(" (" + destination + ")"))
			}

		case *ast.Image:
			buf.WriteString(r.inline(child))
			buf.WriteString(DimmedColor(" (" + string(child.Destination) + ")"))

		case *ast.AutoLink:
			buf.WriteString(Underline(string(child.URL(r.source))))

		case *ast.RawHTML:
			for i := range child.Segments.Len() {
				segment := child.Segments.At(i)
				buf.Write(segment.Value(r.source))
			}

		default:
			r.writeInline(buf, child)
		}
	}
}

// rawText returns the literal text of a code span. A line ending inside one
// is a space, and no escape in it is resolved.
func (r *markdownRenderer) rawText(node ast.Node) string {
	var buf strings.Builder
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		if text, ok := child.(*ast.Text); ok {
			buf.Write(text.Segment.Value(r.source))
		}
	}
	return strings.ReplaceAll(buf.String(), "\n", " ")
}

// literal resolves the backslash escapes and character references which the
// parser leaves in the source, unless the text is raw.
func (r *markdownRenderer) literal(value []byte, raw bool) string {
	if raw {
		return string(value)
	}
	return string(util.UnescapePunctuations(util.ResolveEntityNames(util.ResolveNumericReferences(value))))
}

// unstyled removes the styling from text. A lipgloss style applied over an
// escape sequence prints the sequence as visible characters.
func unstyled(text string) string {
	return ansi.Strip(text)
}
