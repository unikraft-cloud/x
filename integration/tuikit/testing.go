// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package tuikit

import (
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
// it, with WaitMatch, before reading a snapshot.
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
