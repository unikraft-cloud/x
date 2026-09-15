// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"cmp"
	"context"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/reeflective/readline"
)

const (
	// wordBreaks end a word outside quotes.
	wordBreaks = " \t|;&(<>"

	// bareSpecial is what a name needs escaping for, bare on a command line.
	bareSpecial = " \t|;&()<>'\"$`\\*?[]#~!{}"

	// quotedSpecial is what a name needs escaping for inside double quotes.
	quotedSpecial = "\"$`\\"
)

func (s *session) completer(ctx context.Context) func(line []rune, cursor int) readline.Completions {
	return func(line []rune, cursor int) readline.Completions {
		start, matches := s.complete(ctx, line, cursor)
		if len(matches) == 0 {
			return readline.Completions{}
		}

		comps := readline.CompleteValues(matches...).NoSpace('/')
		comps.PREFIX = string(line[start:min(cursor, len(line))])
		return comps
	}
}

// complete is what the word under the cursor could be, each candidate spelling
// the whole word, from the rune it starts at
func (s *session) complete(ctx context.Context, line []rune, cursor int) (start int, matches []string) {
	cursor = min(cursor, len(line))
	head := string(line[:cursor])
	w := wordAt(head)

	if strings.TrimSpace(head[:len(head)-len(w.raw)]) == "" && !strings.Contains(w.text, "/") {
		matches = s.commandMatches(ctx, w)
	} else {
		matches = s.pathMatches(ctx, w)
	}
	// wordAt counts bytes; a caller editing a line counts runes.
	return cursor - utf8.RuneCountInString(w.raw), matches
}

// word is the one under the cursor: as typed, and as the shell would take it
type word struct {
	raw   string
	text  string
	quote byte // the quote raw opens with, or 0

	// dirEnd is how much of raw spells the directory, up to the last slash.
	dirEnd int
}

// wordAt finds the word the cursor is on
func wordAt(head string) word {
	var (
		w       word
		start   int
		quote   byte
		escaped bool
		text    strings.Builder
	)
	for i := 0; i < len(head); i++ {
		c := head[i]
		switch {
		case escaped:
			escaped = false
			text.WriteByte(c)
		case c == '\\' && quote != '\'':
			escaped = true
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				text.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
		case strings.IndexByte(wordBreaks, c) >= 0:
			start = i + 1
			text.Reset()
			w.dirEnd = 0
		default:
			text.WriteByte(c)
		}
		// A slash is a slash however it is written; the last one ends the directory.
		if c == '/' {
			w.dirEnd = i + 1 - start
		}
	}
	w.raw = head[start:]
	w.text = text.String()
	if w.raw != "" && (w.raw[0] == '\'' || w.raw[0] == '"') {
		w.quote = w.raw[0]
	}
	return w
}

// spell writes name the way the word is written, so that it reads back as name
func (w word) spell(name string, dir bool) string {
	var out strings.Builder

	quote := w.quote
	if quote == 0 && strings.ContainsRune(name, '\n') {
		// A newline cannot be escaped bare; open a quote for it.
		quote = '\''
		out.WriteByte(quote)
	}

	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case quote == '\'' && c == '\'':
			out.WriteString(`'\''`)
		case quote == '"' && strings.IndexByte(quotedSpecial, c) >= 0,
			quote == 0 && strings.IndexByte(bareSpecial, c) >= 0:
			out.WriteByte('\\')
			out.WriteByte(c)
		default:
			out.WriteByte(c)
		}
	}
	switch {
	case dir:
		out.WriteByte('/')
	case quote != 0:
		out.WriteByte(quote)
	}
	return out.String()
}

func (s *session) pathMatches(ctx context.Context, w word) []string {
	dir, prefix := path.Split(w.text)

	entries, err := s.readDir(ctx, s.dir(), cmp.Or(dir, "."))
	if err != nil {
		return nil
	}

	var matches []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		matches = append(matches, w.raw[:w.dirEnd]+w.spell(e.Name(), e.IsDir()))
	}
	return matches
}

func (s *session) commandMatches(ctx context.Context, w word) []string {
	var matches []string
	for _, name := range s.builtinNames() {
		if b := BuiltinMarker + name; strings.HasPrefix(b, w.text) {
			matches = append(matches, w.spell(b, false))
		}
	}
	for _, name := range s.remoteCommands(ctx) {
		if strings.HasPrefix(name, w.text) {
			matches = append(matches, w.spell(name, false))
		}
	}
	return matches
}

func (s *session) remoteCommands(ctx context.Context) []string {
	if s.commands != nil {
		return s.commands
	}

	out, err := s.script(ctx, commandsScript)
	if err != nil {
		return nil
	}

	commands := []string{}
	seen := map[string]bool{}
	for name := range strings.SplitSeq(out, "\n") {
		if name = strings.TrimSpace(name); name != "" && !seen[name] {
			seen[name] = true
			commands = append(commands, name)
		}
	}
	slices.Sort(commands)
	s.commands = commands
	return s.commands
}
