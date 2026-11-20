// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2024, Unikraft GmbH and The KraftKit Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package buildfs

import (
	"io"
	"io/fs"

	"github.com/unikraft/go-archivefs/cpio"
)

// CreateCPIO creates a CPIO filesystem from the given fs.FS.
func CreateCPIO(w io.Writer, fsys fs.FS, opts ...CreateOption) error {
	c := createOptions{}
	for _, opt := range opts {
		opt(&c)
	}
	cpioOpts := []cpio.CreateOption{cpio.WithAllRoot(c.allRoot)}
	if c.fileInfoMap != nil {
		fileInfoMap := make(map[string]cpio.FileInfo, len(c.fileInfoMap))
		for path, info := range c.fileInfoMap {
			fileInfoMap[path] = cpio.FileInfo{Uid: info.Uid, Gid: info.Gid, Mode: info.Mode}
		}
		cpioOpts = append(cpioOpts, cpio.WithFileInfoMap(fileInfoMap))
	}
	return cpio.Create(w, fsys, cpioOpts...)
}
