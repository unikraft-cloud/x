// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package tuikit

import (
	"errors"
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
// "ctrl+c", "?". The screen follows later, so wait for what the key puts on
// it, with WaitFor, before reading a snapshot.
func (r *Runner) Press(t testing.TB, keys ...string) {
	t.Helper()

	for _, spec := range keys {
		key, err := ParseKey(spec)
		if err != nil {
			t.Fatalf("tuikit: press %q: %v", spec, err)
		}
		if err := r.PressKeys(key); err != nil {
			t.Fatalf("tuikit: press %q: %v", spec, err)
		}
	}
}

// Type types text, as a terminal sends it: a new line in it is not the
// enter key.
func (r *Runner) Type(t testing.TB, text string) {
	t.Helper()

	if err := r.TypeText(text); err != nil {
		t.Fatalf("tuikit: type %q: %v", text, err)
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

	err := r.WaitUntil(r.timeout, pred)
	if errors.Is(err, ErrStopped) {
		t.Fatalf("tuikit: waiting %s: %v\n\nlast snapshot:\n%s", what, err, r.last())
	}
	if err != nil {
		t.Fatalf("tuikit: gave up waiting %s after %s\n\nlast snapshot:\n%s", what, r.timeout, r.last())
	}

	return r.last()
}

func quote(text string) string {
	return `"` + text + `"`
}
