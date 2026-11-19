// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package io

import (
	"context"
	"io"
)

// ContextReader returns a Reader that does not read when ctx is done, and returns ctx.Err().
func ContextReader(ctx context.Context, r io.Reader) io.Reader {
	return ctxReader{ctx, r}
}

// ContextWriter returns a Writer that does not write when ctx is done, and returns ctx.Err().
func ContextWriter(ctx context.Context, w io.Writer) io.Writer {
	return ctxWriter{ctx, w}
}

// ContextWriterAt is ContextWriter for a WriterAt, and keeps Truncate when w has it.
func ContextWriterAt(ctx context.Context, w io.WriterAt) io.WriterAt {
	if t, ok := w.(interface{ Truncate(int64) error }); ok {
		return ctxTruncWriterAt{ctxWriterAt{ctx, w}, t}
	}
	return ctxWriterAt{ctx, w}
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (r ctxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

type ctxWriter struct {
	ctx context.Context
	w   io.Writer
}

func (w ctxWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.w.Write(p)
}

type ctxWriterAt struct {
	ctx context.Context
	w   io.WriterAt
}

func (w ctxWriterAt) WriteAt(p []byte, off int64) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.w.WriteAt(p, off)
}

type ctxTruncWriterAt struct {
	ctxWriterAt
	t interface{ Truncate(int64) error }
}

func (w ctxTruncWriterAt) Truncate(size int64) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	return w.t.Truncate(size)
}
