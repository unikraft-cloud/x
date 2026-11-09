// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package mergefs

import (
	"context"
	"io"
	"io/fs"

	xio "unikraft.com/x/io"
)

// ctxFS stops a writer when the context ends.
type ctxFS struct {
	*FS
	ctx context.Context
}

func (f ctxFS) Open(name string) (fs.File, error) {
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	file, err := f.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return ctxFile{file, xio.ContextReader(f.ctx, file)}, nil
}

// ctxFile stops a read when the context ends, also where the EROFS writer reads a file without a write.
type ctxFile struct {
	fs.File
	r io.Reader
}

func (f ctxFile) Read(p []byte) (int, error) {
	return f.r.Read(p)
}
