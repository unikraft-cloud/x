// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package mergefs

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/unikraft/go-archivefs/cpio"
	"github.com/unikraft/go-archivefs/erofs"

	xio "unikraft.com/x/io"
)

// EROFS merges the layers and writes a reproducible EROFS image of the result to dst.
func EROFS(ctx context.Context, dst io.WriterAt, layers []Layer, opts ...Option) error {
	fsys, err := Merge(ctx, layers, opts...)
	if err != nil {
		return err
	}
	err = erofs.Create(xio.ContextWriterAt(ctx, dst), ctxFS{fsys, ctx}, erofs.WithUUID([16]byte{}), erofs.WithBuildTime(time.Unix(0, 0)))
	if err == nil {
		err = ctx.Err()
	}
	return errors.Join(err, fsys.Close())
}

// CPIO merges the layers and writes the result to dst as an SVR4 CPIO archive, without xattrs.
func CPIO(ctx context.Context, dst io.Writer, layers []Layer, opts ...Option) error {
	fsys, err := Merge(ctx, layers, opts...)
	if err != nil {
		return err
	}
	err = cpio.Create(xio.ContextWriter(ctx, dst), ctxFS{fsys, ctx})
	if err == nil {
		err = ctx.Err()
	}
	return errors.Join(err, fsys.Close())
}
