// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package io

import (
	"bytes"
	"errors"
	"testing"
)

func TestNopWriteCloserWritesThrough(t *testing.T) {
	var buf bytes.Buffer
	wc := NopWriteCloser(&buf)

	n, err := wc.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 5 {
		t.Fatalf("Write returned %d, want 5", n)
	}
	if got := buf.String(); got != "hello" {
		t.Fatalf("wrote %q, want %q", got, "hello")
	}
}

func TestNopWriteCloserCloseDoesNotClose(t *testing.T) {
	w := &closeCounter{}
	wc := NopWriteCloser(w)

	if err := wc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Closing again must stay a no-op, and must never reach the wrapped
	// writer.
	if err := wc.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if w.closes != 0 {
		t.Fatalf("wrapped writer was closed %d times, want 0", w.closes)
	}
}

// closeCounter records Close calls so a leaked close is visible.
type closeCounter struct {
	closes int
}

func (c *closeCounter) Write(p []byte) (int, error) { return len(p), nil }

func (c *closeCounter) Close() error {
	c.closes++
	return errors.New("wrapped writer must not be closed")
}
