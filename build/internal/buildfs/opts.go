// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2024, Unikraft GmbH and The KraftKit Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package buildfs

import "github.com/unikraft/go-archivefs/erofs"

// createOptions holds shared options for filesystem creation.
type createOptions struct {
	allRoot     bool
	fileInfoMap map[string]erofs.FileInfo
	pad         int64
	compress    bool
}

// CreateOption is an option for CreateCPIO, CreateEROFS and Package.
type CreateOption func(*createOptions)

// WithPad makes Package pad the output to a multiple of pad bytes.
func WithPad(pad int64) CreateOption {
	return func(co *createOptions) {
		co.pad = pad
	}
}

// WithCompress makes Package gzip a cpio archive.
func WithCompress(compress bool) CreateOption {
	return func(co *createOptions) {
		co.compress = compress
	}
}

// WithAllRoot toggles whether all file ownership should be set to root:root
// instead of the original file permissions.
func WithAllRoot(allRoot bool) CreateOption {
	return func(co *createOptions) {
		co.allRoot = allRoot
	}
}

// WithFileInfoMap sets the owner and mode of each path in the map, keyed as
// "/etc/passwd". WithAllRoot still sets the owner to root.
func WithFileInfoMap(fileInfoMap map[string]erofs.FileInfo) CreateOption {
	return func(co *createOptions) {
		co.fileInfoMap = fileInfoMap
	}
}
