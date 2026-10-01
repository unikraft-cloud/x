// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"cmp"
	"context"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/reeflective/readline"
)

const (
	// completionTimeout is how long a Tab waits on the instance before offering nothing.
	completionTimeout = time.Second

	// wordBreaks end a word outside quotes.
	wordBreaks = " \t|;&(<>"

	// bareSpecial is what a name needs escaping for, bare on a command line.
	bareSpecial = " \t|;&()<>'\"$`\\*?[]#~!{}"

	// quotedSpecial is what a name needs escaping for inside double quotes.
	quotedSpecial = "\"$`\\"
)

func (s *state) completer(ctx context.Context) func(line []rune, cursor int) readline.Completions {
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
func (s *state) complete(ctx context.Context, line []rune, cursor int) (start int, matches []string) {
	ctx, cancel := context.WithTimeout(WithDetached(ctx), completionTimeout)
	defer cancel()

	cursor = min(cursor, len(line))
	head := string(line[:cursor])
	w := wordAt(head)

	before := strings.TrimSpace(head[:len(head)-len(w.raw)])
	command := before == "" || strings.ContainsAny(before[len(before)-1:], "|;&(")
	if command && !strings.Contains(w.text, "/") {
		matches = s.commandMatches(ctx, w)
	} else {
		matches = s.pathMatches(ctx, w)
	}
	// wordAt counts bytes; a caller editing a line counts runes.
	return cursor - utf8.RuneCountInString(w.raw), matches
}

// lead is what a candidate keeps of the word as typed: the directory, or a bare opening quote.
func (w word) lead() string {
	if w.dirEnd == 0 && w.quote != 0 {
		return w.raw[:1]
	}
	return w.raw[:w.dirEnd]
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
	for i := range len(head) {
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
	w.quote = quote
	return w
}

// spell writes name the way the word is written, so that it reads back as name
func (w word) spell(name string, dir bool) string {
	var out strings.Builder

	quote, ours := w.quote, false
	if quote == 0 && strings.ContainsRune(name, '\n') {
		// A newline cannot be escaped bare; open a quote for it.
		quote, ours = '\'', true
		out.WriteByte(quote)
	}

	for i := range len(name) {
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
	case dir && ours:
		out.WriteByte(quote)
		out.WriteByte('/')
	case dir:
		out.WriteByte('/')
	case quote != 0:
		out.WriteByte(quote)
	}
	return out.String()
}

func (s *state) pathMatches(ctx context.Context, w word) []string {
	dir, prefix := path.Split(w.text)

	entries, err := s.cfg.Transport.ReadDir(ctx, s.dir(), cmp.Or(dir, "."))
	if err != nil {
		return nil
	}

	var matches []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		matches = append(matches, w.lead()+w.spell(e.Name(), e.IsDir()))
	}
	return matches
}

func (s *state) commandMatches(ctx context.Context, w word) []string {
	var matches []string
	for _, name := range s.builtinNames() {
		if b := BuiltinMarker + name; strings.HasPrefix(b, w.text) {
			matches = append(matches, w.lead()+w.spell(b, false))
		}
	}
	for _, name := range s.remoteCommands(ctx) {
		if strings.HasPrefix(name, w.text) {
			matches = append(matches, w.lead()+w.spell(name, false))
		}
	}
	return matches
}

// remoteCommands is what the instance has to offer, asked again after each line runs.
func (s *state) remoteCommands(ctx context.Context) []string {
	if s.commands != nil {
		return s.commands
	}
	if s.noShell {
		return nil
	}

	commands, err := s.cfg.Transport.Commands(ctx)
	if err != nil {
		return nil
	}
	s.commands = commands
	return s.commands
}
