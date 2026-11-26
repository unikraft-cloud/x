// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package io_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	xio "unikraft.com/x/io"
)

func TestContextStopsWhenCanceled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// open wraps a stream with ctx. step moves "data" through it once, and got returns the data that went through.
		open func(t *testing.T, ctx context.Context) (step func() (int, error), got func() string)
	}{
		{
			name: "ContextReader",
			open: func(_ *testing.T, ctx context.Context) (func() (int, error), func() string) {
				r := xio.ContextReader(ctx, strings.NewReader("datadata"))
				var out []byte
				return func() (int, error) {
					p := make([]byte, len("data"))
					n, err := r.Read(p)
					out = append(out, p[:n]...)
					return n, err
				}, func() string { return string(out) }
			},
		},
		{
			name: "ContextWriter",
			open: func(_ *testing.T, ctx context.Context) (func() (int, error), func() string) {
				var buf bytes.Buffer
				w := xio.ContextWriter(ctx, &buf)
				return func() (int, error) { return w.Write([]byte("data")) }, buf.String
			},
		},
		{
			name: "ContextWriterAt",
			open: func(t *testing.T, ctx context.Context) (func() (int, error), func() string) {
				f := createFile(t)
				w := xio.ContextWriterAt(ctx, f)
				var off int64
				return func() (int, error) {
						n, err := w.WriteAt([]byte("data"), off)
						off += int64(n)
						return n, err
					}, func() string {
						b, err := os.ReadFile(f.Name())
						require.NoError(t, err)
						return string(b)
					}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			step, got := tt.open(t, ctx)

			n, err := step()
			require.NoError(t, err)
			require.Equal(t, len("data"), n)

			cancel()
			n, err = step()
			require.ErrorIs(t, err, context.Canceled)
			require.Zero(t, n)
			require.Equal(t, "data", got())
		})
	}
}

func createFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "file"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.Close()) })
	return f
}
