// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package tuikit

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Start starts a Runner for model, and stops it when the test ends.
func Start(t testing.TB, model tea.Model, opts ...Option) *Runner {
	t.Helper()

	r := New(model, opts...)
	t.Cleanup(r.Stop)

	return r
}

// Press presses each of keys, written as ParseKey reads them: "enter",
// "ctrl+c", "?".
func (r *Runner) Press(t testing.TB, keys ...string) {
	t.Helper()

	for _, spec := range keys {
		key, err := ParseKey(spec)
		if err != nil {
			t.Fatalf("tuikit: press %q: %v", spec, err)
		}
		r.PressKeys(key)
	}
}

// Type presses the key of each character of text.
func (r *Runner) Type(t testing.TB, text string) {
	t.Helper()

	for _, c := range text {
		r.PressKeys(tea.Key{Code: c, Text: string(c)})
	}
}

// WaitFor waits for text to be on the screen, and returns the snapshot that
// has it. It fails the test with the last snapshot if the text never shows.
func (r *Runner) WaitFor(t testing.TB, text string) string {
	t.Helper()

	return r.wait(t, "for "+quote(text), func(s string) bool { return strings.Contains(s, text) })
}

// WaitForGone is WaitFor, for text to be gone from the screen.
func (r *Runner) WaitForGone(t testing.TB, text string) string {
	t.Helper()

	return r.wait(t, "for "+quote(text)+" to be gone", func(s string) bool { return !strings.Contains(s, text) })
}

// WaitForFunc is WaitFor, for a snapshot that pred accepts.
func (r *Runner) WaitForFunc(t testing.TB, pred func(snapshot string) bool) string {
	t.Helper()

	return r.wait(t, "for the screen", pred)
}

func (r *Runner) wait(t testing.TB, what string, pred func(string) bool) string {
	t.Helper()

	var snap string
	err := r.WaitUntil(r.timeout, 0, func(s string) bool {
		snap = s
		return pred(s)
	})
	if err != nil {
		t.Fatalf("tuikit: gave up waiting %s after %s\n\nlast snapshot:\n%s", what, r.timeout, snap)
	}

	return snap
}

func quote(text string) string {
	return `"` + text + `"`
}
