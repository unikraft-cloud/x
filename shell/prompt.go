// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/reeflective/readline"
	"mvdan.cc/sh/v3/syntax"
)

const (
	// interruptKey is ^C as readline reads it.
	interruptKey = 0x03

	// wakeEvery is how often a cancelled read knocks on readline again: one ^C
	// only closes an incremental search, the next one ends the read.
	wakeEvery = 50 * time.Millisecond
)

// prompt is the line editor the session reads from; the editing itself belongs to readline.
type prompt struct {
	rl      *readline.Shell
	history *sessionHistory
}

func (s *session) newPrompt(ctx context.Context) *prompt {
	rl := readline.NewShell()
	// Bytes above 0x7f are typed accents, not meta keys.
	_ = rl.Config.Set("convert-meta", false)
	// The session prints its own ^C.
	_ = rl.Config.Set("echo-control-characters", false)
	history := &sessionHistory{}

	rl.History.Add("session", history)
	rl.Prompt.Primary(func() string { return s.prompt(false) })
	rl.Prompt.Secondary(func() string { return s.prompt(true) })

	rl.AcceptMultiline = acceptMultiline
	rl.SyntaxHighlighter = func(line []rune) string {
		return s.paint(highlight(string(line), s.isBuiltinName))
	}
	rl.Completer = s.completer(ctx)

	return &prompt{rl: rl, history: history}
}

// readLine reads a line, or fakes a ^C to get readline back when ctx ends first.
func (p *prompt) readLine(ctx context.Context) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
			return
		}
		// Feed is readline's way in from another goroutine, though it races the
		// unlocked reads of the same key queue upstream; nothing else wakes it.
		knock := time.NewTicker(wakeEvery)
		defer knock.Stop()
		for {
			p.rl.Keys.Feed(true, interruptKey)
			p.rl.Keys.RequestRefresh()
			select {
			case <-knock.C:
			case <-done:
				return
			}
		}
	}()

	line, err := p.rl.Readline()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return line, err
}

func acceptMultiline(line []rune) bool {
	_, err := syntax.NewParser().Parse(strings.NewReader(string(line)), "")
	return !syntax.IsIncomplete(err)
}

func (s *session) isBuiltinName(name string) bool {
	return slices.Contains(s.builtinNames(), name)
}

// sessionHistory is what the session has run, kept for as long as it lasts.
type sessionHistory struct {
	mu    sync.RWMutex
	lines []string
}

func (h *sessionHistory) Write(line string) (int, error) {
	if line = strings.TrimSpace(line); line == "" {
		return h.Len(), nil
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if n := len(h.lines); n == 0 || h.lines[n-1] != line {
		h.lines = append(h.lines, line)
	}
	return len(h.lines), nil
}

func (h *sessionHistory) GetLine(pos int) (string, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if pos < 0 || pos >= len(h.lines) {
		return "", nil
	}
	return h.lines[pos], nil
}

func (h *sessionHistory) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.lines)
}

func (h *sessionHistory) Dump() any { return h.recalled() }

func (h *sessionHistory) recalled() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return slices.Clone(h.lines)
}
