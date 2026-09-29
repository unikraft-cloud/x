// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !js

package io_test

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"

	xio "unikraft.com/x/io"
)

// fakeFdReader mimics an *os.File-like reader that exposes Fd(). It reads from
// an in-memory buffer so tests don't touch real file descriptors.
type fakeFdReader struct {
	bytes.Buffer
	fd uintptr
}

func (r *fakeFdReader) Fd() uintptr { return r.fd }

// TestIsTTYWriter_SeesThroughWrappers guards against wrappers hiding Fd()
// from TTY detection.
func TestIsTTYWriter_SeesThroughWrappers(t *testing.T) {
	t.Parallel()

	// Use os.Stdin (or any real *os.File) so term.IsTerminal has a real fd to
	// inspect. The result varies by environment, but consistency across
	// wrappers is what we're asserting.
	base := os.Stdin
	want := xio.IsTTYWriter(base)

	wrappers := []struct {
		name string
		w    io.Writer
	}{
		{"colorprofile.Writer", &colorprofile.Writer{Forward: base}},
		{"double-wrapped colorprofile.Writer", &colorprofile.Writer{
			Forward: &colorprofile.Writer{Forward: base},
		}},
	}

	for _, w := range wrappers {
		t.Run(w.name, func(t *testing.T) {
			t.Parallel()
			if got := xio.IsTTYWriter(w.w); got != want {
				t.Errorf("IsTTYWriter(%s) = %v, want %v (matching unwrapped base)", w.name, got, want)
			}
		})
	}
}

func TestIsTTYWriter_NonFdWriter(t *testing.T) {
	t.Parallel()

	if xio.IsTTYWriter(&bytes.Buffer{}) {
		t.Error("IsTTYWriter(*bytes.Buffer) = true, want false")
	}
	if xio.IsTTYWriter(&colorprofile.Writer{Forward: &bytes.Buffer{}}) {
		t.Error("IsTTYWriter(colorprofile wrapping *bytes.Buffer) = true, want false")
	}
}

func TestTermWidth_HonorsCOLUMNS(t *testing.T) {
	// Not t.Parallel() because we mutate the COLUMNS env var.
	t.Setenv("COLUMNS", "123")

	tests := []struct {
		name string
		w    io.Writer
	}{
		{"plain writer", &bytes.Buffer{}},
		// Use a clearly invalid fd so the width query reliably falls through
		// to the COLUMNS env var instead of accidentally consulting a real
		// file descriptor (e.g. stdin) that may happen to be a TTY.
		{"fd writer", &fakeFdWriter{fd: ^uintptr(0)}},
		{"colorprofile-wrapped fd writer", &colorprofile.Writer{Forward: &fakeFdWriter{fd: ^uintptr(0)}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := xio.TermWidth(tt.w); got != 123 {
				t.Errorf("TermWidth(%s) = %d, want 123 (from COLUMNS)", tt.name, got)
			}
		})
	}
}

func TestIsTTYReader_NonFdReader(t *testing.T) {
	t.Parallel()

	if xio.IsTTYReader(&bytes.Buffer{}) {
		t.Error("IsTTYReader(*bytes.Buffer) = true, want false")
	}
	if xio.IsTTYReader(strings.NewReader("")) {
		t.Error("IsTTYReader(*strings.Reader) = true, want false")
	}
}

// TestIsTTYReader_MatchesWriterOnSameFd pins that the reader and writer forms
// agree for one underlying file descriptor.
func TestIsTTYReader_MatchesWriterOnSameFd(t *testing.T) {
	t.Parallel()

	if got, want := xio.IsTTYReader(os.Stdin), xio.IsTTYWriter(os.Stdin); got != want {
		t.Errorf("IsTTYReader(os.Stdin) = %v, IsTTYWriter(os.Stdin) = %v; want equal", got, want)
	}
}

// TestIsTTYReader_KeepsTheDeadline pins that asking does not cost the file its
// read deadline, which Fd would: a blocked read on it must stay cancellable.
func TestIsTTYReader_KeepsTheDeadline(t *testing.T) {
	t.Parallel()

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()

	if xio.IsTTYReader(pr) {
		t.Error("IsTTYReader(pipe) = true, want false")
	}
	if err := pr.SetReadDeadline(time.Now()); err != nil {
		t.Errorf("SetReadDeadline after IsTTYReader: %v, want nil", err)
	}
}

func TestIsTTYReader_NotATerminalFd(t *testing.T) {
	t.Parallel()

	if xio.IsTTYReader(&fakeFdReader{fd: ^uintptr(0)}) {
		t.Error("IsTTYReader(invalid fd) = true, want false")
	}
}
