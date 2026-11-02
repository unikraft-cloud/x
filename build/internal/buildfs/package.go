// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package buildfs

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/unikraft/go-archivefs"
	"unikraft.com/x/kraftfile"
	"unikraft.com/x/log"
)

// ErrTooLarge tells that a rootfs or a rom is larger than its size limit.
var ErrTooLarge = errors.New("rootfs is too large")

// Package writes src to dst in format, padded as WithPad asks.
func Package(ctx context.Context, format kraftfile.FsType, dst *os.File, src fs.FS, opts ...CreateOption) error {
	c := createOptions{}
	for _, opt := range opts {
		opt(&c)
	}

	log.G(ctx).
		Debug().
		Str("format", string(format)).
		Msg("packaging rootfs")

	switch format {
	case kraftfile.FsTypeCpio:
		var gw *gzip.Writer
		var w io.Writer = dst
		if c.compress {
			gw = gzip.NewWriter(w)
			w = gw
		}

		if err := CreateCPIO(ctx, w, src, opts...); err != nil {
			return fmt.Errorf("could not create CPIO archive: %w", err)
		}

		if gw != nil {
			if err := gw.Close(); err != nil {
				return fmt.Errorf("could not close gzip writer: %w", err)
			}
		}
	case kraftfile.FsTypeErofs:
		if c.compress {
			log.G(ctx).Warn().Msg("compression is not supported for EROFS, ignoring compress option")
		}

		if err := CreateEROFS(dst, src, opts...); err != nil {
			return fmt.Errorf("could not create EroFS archive: %w", err)
		}
	default:
		return fmt.Errorf("unknown filesystem type %q", format)
	}

	if err := PadFile(dst, c.pad); err != nil {
		return err
	}

	if err := dst.Sync(); err != nil {
		return fmt.Errorf("could not sync file: %w", err)
	}

	return nil
}

// PadFile pads f up to a multiple of pad bytes.
func PadFile(f *os.File, pad int64) error {
	if pad <= 0 {
		return nil
	}

	pos, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("could not seek to end of file: %w", err)
	}
	if rem := pos % pad; rem != 0 {
		padding := make([]byte, pad-rem)
		if _, err := f.Write(padding); err != nil {
			return fmt.Errorf("could not pad file to page alignment: %w", err)
		}
	}

	return nil
}

// CheckSize returns ErrTooLarge if the regular files of fsys, each file
// with hardlinks once, hold more than limit. A tarball can claim more than it holds.
func CheckSize(fsys fs.FS, limit int64) error {
	if limit <= 0 {
		return nil
	}

	var total int64
	linked := map[uint64]bool{}
	return fs.WalkDir(fsys, ".", func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if sys := info.Sys(); archivefs.GetNlink(sys) > 1 {
			ino := archivefs.GetIno(sys)
			if linked[ino] {
				return nil
			}
			linked[ino] = true
		}
		if total += info.Size(); total > limit {
			return fmt.Errorf("%w: more than %d bytes", ErrTooLarge, limit)
		}
		return nil
	})
}
