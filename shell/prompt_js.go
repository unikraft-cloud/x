// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build js

package shell

import (
	"context"
	"errors"
)

// errInterrupt is what readLine returns for a ^C.
var errInterrupt = errors.New("interrupt")

// prompt is the line editor the session reads from.  A js host has no terminal
// for it, so the host drives a [Session] with its own line editing instead.
type prompt struct{}

// promptConfig is what the prompt needs from the session, and nothing else of it.
type promptConfig struct {
	history   *sessionHistory
	prompt    func(continuation bool) string
	paint     func(string) string
	isBuiltin func(string) bool
}

func newPrompt(promptConfig) *prompt { return &prompt{} }

func (p *prompt) readLine(ctx context.Context) (string, error) {
	return "", errNotATerminal
}
