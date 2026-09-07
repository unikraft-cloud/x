// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"strings"

	"charm.land/lipgloss/v2"
)

const shellSpecial = "|&;<>()$"

// highlight colours a line as typed: strings, shell specials, and a builtin
// where a command goes. Every byte of line comes back, only wrapped.
func highlight(line string, isBuiltin func(string) bool) string {
	var out strings.Builder
	out.Grow(len(line) * 2)

	command := true
	for i := 0; i < len(line); {
		switch c := line[i]; {
		case c == '\\':
			end := min(i+2, len(line))
			out.WriteString(line[i:end])
			i = end
			command = false

		case c == '\'' || c == '"':
			end := len(line)
			for j := i + 1; j < len(line); j++ {
				if line[j] == c {
					end = j + 1
					break
				}
				if c == '"' && line[j] == '\\' {
					j++
				}
			}
			out.WriteString(renderLines(highlightStringStyle, line[i:end]))
			i = end
			command = false

		case strings.IndexByte(shellSpecial, c) >= 0:
			out.WriteString(highlightSpecialStyle.Render(string(c)))
			i++
			command = c != '$' && c != '<' && c != '>'

		case c == ' ' || c == '\t' || c == '\n':
			end := i + 1
			for end < len(line) && (line[end] == ' ' || line[end] == '\t' || line[end] == '\n') {
				end++
			}
			out.WriteString(line[i:end])
			if strings.IndexByte(line[i:end], '\n') >= 0 {
				command = true
			}
			i = end

		case c == BuiltinMarker[0]:
			end := i + wordLen(line[i:])
			word := line[i:end]
			if command && isBuiltin != nil && isBuiltin(strings.TrimPrefix(word, BuiltinMarker)) {
				out.WriteString(highlightBuiltinStyle.Render(word))
			} else {
				out.WriteString(word)
			}
			i = end
			command = false

		default:
			end := i + wordLen(line[i:])
			out.WriteString(line[i:end])
			i = end
			command = false
		}
	}
	return out.String()
}

// renderLines styles each physical line on its own: handed a newline, lipgloss
// pads every line to the widest, and the prompt would paint spaces the user
// never typed.
func renderLines(style lipgloss.Style, s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = style.Render(line)
	}
	return strings.Join(lines, "\n")
}

func wordLen(s string) int {
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c == '\\' {
			i++
			continue
		}
		if c == '\'' || c == '"' || c == ' ' || c == '\t' || c == '\n' || strings.IndexByte(shellSpecial, c) >= 0 {
			return i
		}
	}
	return len(s)
}
