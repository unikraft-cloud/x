// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build js

package imagespec

import (
	"context"
	"fmt"
	"io"

	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// errLocalImages is returned on js, which has no local content store.
var errLocalImages = fmt.Errorf("local image files are not supported on js: %w", errdefs.ErrNotImplemented)

func LoadOCILayout(ctx context.Context, path string, desc ocispec.Descriptor, platform platforms.MatchComparer) (*Image, error) {
	return nil, errLocalImages
}

func LoadAllOCILayouts(ctx context.Context, path string, desc ocispec.Descriptor, platform platforms.MatchComparer) ([]*Image, error) {
	return nil, errLocalImages
}

func LoadOCILayoutNamed(ctx context.Context, path string, tag string, platform platforms.MatchComparer) (*Image, error) {
	return nil, errLocalImages
}

func LoadAllOCILayoutsNamed(ctx context.Context, path string, tag string, platform platforms.MatchComparer) ([]*Image, error) {
	return nil, errLocalImages
}

func SaveOCILayout(ctx context.Context, path string, tag string, image ...*Image) (ocispec.Descriptor, error) {
	return ocispec.Descriptor{}, errLocalImages
}

func SaveOCILayoutNamed(ctx context.Context, path string, tag string, image ...*Image) (ocispec.Descriptor, error) {
	return ocispec.Descriptor{}, errLocalImages
}

func DeleteOCILayoutNamed(path string, tag string) error {
	return errLocalImages
}

func LoadTarball(ctx context.Context, tarballPath string, platform platforms.MatchComparer) (*Image, error) {
	return nil, errLocalImages
}

func LoadAllTarballs(ctx context.Context, tarballPath string, platform platforms.MatchComparer) ([]*Image, error) {
	return nil, errLocalImages
}

func LoadTarballReader(ctx context.Context, r io.Reader, platform platforms.MatchComparer) (*Image, error) {
	return nil, errLocalImages
}

func LoadAllTarballReaders(ctx context.Context, r io.Reader, platform platforms.MatchComparer) ([]*Image, error) {
	return nil, errLocalImages
}

func SaveTarball(ctx context.Context, tarballPath string, image ...*Image) error {
	return errLocalImages
}

func SaveTarballWriter(ctx context.Context, w io.Writer, image ...*Image) error {
	return errLocalImages
}
