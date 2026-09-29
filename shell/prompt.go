// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !js

package shell

import (
	"context"
	"time"

	"github.com/reeflective/readline"
)

const (
	// interruptKey is ^C as readline reads it.
	interruptKey = 0x03

	// wakeEvery is how often a cancelled read knocks on readline again
	wakeEvery = 50 * time.Millisecond
)

// errInterrupt is what readLine returns for a ^C.
var errInterrupt = readline.ErrInterrupt

// prompt is the line editor the session reads from; the editing itself belongs to readline.
type prompt struct {
	rl *readline.Shell
}

// promptConfig is what the prompt needs from the session, and nothing else of it.
type promptConfig struct {
	history   *sessionHistory
	prompt    func(continuation bool) string
	paint     func(string) string
	isBuiltin func(string) bool
	complete  func(line []rune, cursor int) readline.Completions
}

func newPrompt(cfg promptConfig) *prompt {
	rl := readline.NewShell()
	// Bytes above 0x7f are typed accents, not meta keys.
	_ = rl.Config.Set("convert-meta", false)
	_ = rl.Config.Set("output-meta", true)
	// The session prints its own ^C.
	_ = rl.Config.Set("echo-control-characters", false)

	rl.History.Add("session", cfg.history)
	rl.Prompt.Primary(func() string { return cfg.prompt(false) })
	rl.Prompt.Secondary(func() string { return cfg.prompt(true) })

	rl.AcceptMultiline = acceptMultiline
	rl.SyntaxHighlighter = func(line []rune) string {
		return cfg.paint(highlight(string(line), cfg.isBuiltin))
	}
	if cfg.complete != nil {
		rl.Completer = cfg.complete
	}

	return &prompt{rl: rl}
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
		// Feed is readline's way in from another goroutine
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
