// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH and The KraftKit Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package buildfs

import (
	"io"
	"io/fs"
	"time"

	"github.com/unikraft/go-archivefs/erofs"
)

// CreateEROFS creates an EroFS filesystem image from the given fs.FS.
func CreateEROFS(w io.WriterAt, fsys fs.FS, opts ...CreateOption) error {
	c := createOptions{}
	for _, opt := range opts {
		opt(&c)
	}
	// A zero UUID and build time make the same tree give the same image.
	erofsOpts := []erofs.ErofsCreateOption{
		erofs.WithUUID([16]byte{}),
		erofs.WithBuildTime(time.Unix(0, 0)),
	}
	if c.allRoot {
		erofsOpts = append(erofsOpts, erofs.WithAllRoot(true))
	}
	if c.fileInfoMap != nil {
		erofsOpts = append(erofsOpts, erofs.WithFileInfoMap(c.fileInfoMap))
	}
	return erofs.Create(w, fsys, erofsOpts...)
}
