// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package rootfs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"time"

	"github.com/unikraft/go-archivefs/cpio"
	"github.com/unikraft/go-archivefs/erofs"
)

// EROFS merges the layers and writes a reproducible EROFS image of the result to dst.
func EROFS(ctx context.Context, dst io.WriterAt, layers []Layer, opts ...Option) error {
	fsys, err := Merge(ctx, layers, opts...)
	if err != nil {
		return err
	}
	err = erofs.Create(dst, ctxFS{fsys, ctx}, erofs.WithUUID([16]byte{}), erofs.WithBuildTime(time.Unix(0, 0)))
	return errors.Join(err, fsys.Close())
}

// CPIO merges the layers and writes the result to dst as an SVR4 CPIO archive, without xattrs.
func CPIO(ctx context.Context, dst io.Writer, layers []Layer, opts ...Option) error {
	fsys, err := Merge(ctx, layers, opts...)
	if err != nil {
		return err
	}
	err = cpio.Create(dst, ctxFS{fsys, ctx})
	return errors.Join(err, fsys.Close())
}

// ctxFS stops a writer when the context ends.
type ctxFS struct {
	*FS
	ctx context.Context
}

func (f ctxFS) Open(name string) (fs.File, error) {
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	return f.FS.Open(name)
}
