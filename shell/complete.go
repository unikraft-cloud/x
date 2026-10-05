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
	"time"
	"unicode/utf8"
)

const (
	// completionTimeout is how long a Tab waits on the instance before offering nothing.
	completionTimeout = time.Second

	// wordBreaks end a word outside quotes.
	wordBreaks = " \t\n|;&(<>"

	// separators end a command, so the word after one names the next.
	separators = "\n|;&("

	// bareSpecial is what a name needs escaping for, bare on a command line.
	bareSpecial = " \t|;&()<>'\"$`\\*?[]#~!{}"

	// quotedSpecial is what a name needs escaping for inside double quotes.
	quotedSpecial = "\"$`\\"

	// A menu heads each half of what a command position offers.
	tagBuiltin = "builtins"
	tagCommand = "commands"
)

// commandWords are reserved words a command follows directly.
var commandWords = []string{"!", "{", "if", "then", "elif", "else", "do", "while", "until", "time"}

// candidate is a match as a menu takes it; readline's own type does not build for js.
type candidate struct{ Value, Display, Tag string }

// complete is the word under the cursor as a caller drawing no menu takes it:
// the value each candidate inserts
func (s *state) complete(ctx context.Context, line []rune, cursor int) (start int, matches []string) {
	start, cands, _ := s.completions(ctx, line, cursor)
	for _, c := range cands {
		matches = append(matches, c.Value)
	}
	return start, matches
}

// completions is what the word under the cursor could be, each candidate
// spelling the whole word, from the rune it starts at
func (s *state) completions(ctx context.Context, line []rune, cursor int) (start int, cands []candidate, err error) {
	ctx, cancel := context.WithTimeout(WithDetached(ctx), completionTimeout)
	defer cancel()

	cursor = min(cursor, len(line))
	head := string(line[:cursor])
	w := wordAt(head)

	if len(w.args) == 0 && !strings.Contains(w.text, "/") {
		cands, err = s.commandMatches(ctx, w)
	} else {
		cands, err = s.pathMatches(ctx, w)
	}
	// wordAt counts bytes; a caller editing a line counts runes.
	return cursor - utf8.RuneCountInString(w.raw), cands, err
}

// lead is what a candidate keeps of the word as typed: the directory, and a quote opened after it.
func (w word) lead() string {
	if w.quote != 0 && w.quoteAt >= w.dirEnd {
		return w.raw[:w.dirEnd] + string(w.quote)
	}
	return w.raw[:w.dirEnd]
}

// word is the word under the cursor: raw as typed, text with its quotes and escapes removed
type word struct {
	raw     string
	text    string
	quote   byte     // the quote left open in raw, or 0
	quoteAt int      // where in raw that quote opens
	args    []string // the words before it in its command

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
			quote, w.quoteAt = c, i-start
		case strings.IndexByte(wordBreaks, c) >= 0:
			if i > start {
				w.args = append(w.args, text.String())
			}
			if strings.IndexByte(separators, c) >= 0 {
				w.args = nil
			}
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
	// An assignment or a reserved word before a command is not one of its words.
	for len(w.args) > 0 {
		name, _, assign := strings.Cut(w.args[0], "=")
		if !slices.Contains(commandWords, w.args[0]) && (!assign || !isEnvName(name)) {
			break
		}
		w.args = w.args[1:]
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

func (s *state) pathMatches(ctx context.Context, w word) ([]candidate, error) {
	if s.noShell {
		return nil, ErrNoShell
	}
	dir, prefix := path.Split(w.text)

	entries, err := s.cfg.Transport.ReadDir(ctx, s.dir(), cmp.Or(dir, "."))
	if err != nil {
		return nil, err
	}

	var matches []candidate
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		// A menu reads the name alone; the line takes it spelled out.
		display := e.Name()
		if e.IsDir() {
			display += "/"
		}
		matches = append(matches, candidate{
			Value:   w.lead() + w.spell(e.Name(), e.IsDir()),
			Display: display,
		})
	}
	return matches, nil
}

func (s *state) commandMatches(ctx context.Context, w word) ([]candidate, error) {
	var matches []candidate
	for _, name := range s.builtinNames() {
		if b := BuiltinMarker + name; strings.HasPrefix(b, w.text) {
			matches = append(matches, candidate{
				Value: w.lead() + w.spell(b, false),
				// A menu paints a builtin the way the line does.
				Display: s.paint(highlightBuiltinStyle.Render(b)),
				Tag:     tagBuiltin,
			})
		}
	}

	commands, err := s.remoteCommands(ctx)
	for _, name := range commands {
		if strings.HasPrefix(name, w.text) {
			matches = append(matches, candidate{
				Value:   w.lead() + w.spell(name, false),
				Display: name,
				Tag:     tagCommand,
			})
		}
	}
	return matches, err
}

// remoteCommands is what the instance has to offer, asked again after each line runs.
func (s *state) remoteCommands(ctx context.Context) ([]string, error) {
	if s.commands != nil {
		return s.commands, nil
	}
	if s.noShell {
		return nil, ErrNoShell
	}

	commands, err := s.cfg.Transport.Commands(ctx)
	if err != nil {
		return nil, err
	}
	s.commands = commands
	return s.commands, nil
}
