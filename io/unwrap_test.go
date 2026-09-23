// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package io_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/charmbracelet/colorprofile"

	xio "unikraft.com/x/io"
)

// fakeFdWriter mimics an *os.File-like writer that exposes Fd(). It writes to
// an in-memory buffer so tests don't touch real file descriptors.
type fakeFdWriter struct {
	bytes.Buffer
	fd uintptr
}

func (w *fakeFdWriter) Fd() uintptr { return w.fd }

func TestUnwrap(t *testing.T) {
	t.Parallel()

	plain := &bytes.Buffer{}
	fdw := &fakeFdWriter{fd: 42}

	tests := []struct {
		name string
		in   io.Writer
		want io.Writer
	}{
		{
			name: "plain writer is returned unchanged",
			in:   plain,
			want: plain,
		},
		{
			name: "fd writer is returned unchanged",
			in:   fdw,
			want: fdw,
		},
		{
			name: "colorprofile.Writer is peeled to its Forward",
			in:   &colorprofile.Writer{Forward: fdw},
			want: fdw,
		},
		{
			name: "nested colorprofile.Writer is peeled fully",
			in: &colorprofile.Writer{
				Forward: &colorprofile.Writer{Forward: fdw},
			},
			want: fdw,
		},
		{
			name: "colorprofile.Writer wrapping a non-fd writer still peels",
			in:   &colorprofile.Writer{Forward: plain},
			want: plain,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := xio.Unwrap(tt.in)
			if got != tt.want {
				t.Errorf("Unwrap() = %T(%p), want %T(%p)", got, got, tt.want, tt.want)
			}
		})
	}
}
