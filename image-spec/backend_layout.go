// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !js

package imagespec

import (
	"context"
	"fmt"

	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"unikraft.com/x/image-spec/ociindex"
)

// This file provides functions for loading and saving images to/from OCI layouts.
// This implements the OCI image layout spec at https://github.com/opencontainers/image-spec/blob/main/image-layout.md
//
// This is essentially equivalent to the tarball archive loaders/savers, but
// unpacked and managing an index file. It's very useful for debugging :)

func LoadOCILayout(ctx context.Context, path string, desc ocispec.Descriptor, platform platforms.MatchComparer) (*Image, error) {
	store, err := local.NewStore(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open content store at %q: %w", path, err)
	}
	return LoadContent(ctx, store, desc, platform)
}

func LoadAllOCILayouts(ctx context.Context, path string, desc ocispec.Descriptor, platform platforms.MatchComparer) ([]*Image, error) {
	store, err := local.NewStore(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open content store at %q: %w", path, err)
	}
	return LoadAllContent(ctx, store, desc, platform)
}

func LoadOCILayoutNamed(ctx context.Context, path string, tag string, platform platforms.MatchComparer) (*Image, error) {
	idx := ociindex.NewStoreIndex(path)
	var desc *ocispec.Descriptor
	var err error
	if tag == "" {
		desc, err = idx.GetSingle()
	} else {
		desc, err = idx.Get(tag)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get image descriptor for %q: %w", tag, err)
	}

	store, err := local.NewStore(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open content store at %q: %w", path, err)
	}

	return LoadContent(ctx, store, *desc, platform)
}

func LoadAllOCILayoutsNamed(ctx context.Context, path string, tag string, platform platforms.MatchComparer) ([]*Image, error) {
	idx := ociindex.NewStoreIndex(path)
	var desc *ocispec.Descriptor
	var err error
	if tag == "" {
		desc, err = idx.GetSingle()
	} else {
		desc, err = idx.Get(tag)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get image descriptor for %q: %w", tag, err)
	}

	store, err := local.NewStore(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open content store at %q: %w", path, err)
	}

	return LoadAllContent(ctx, store, *desc, platform)
}

func SaveOCILayout(ctx context.Context, path string, tag string, image ...*Image) (ocispec.Descriptor, error) {
	store, err := local.NewStore(path)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to open content store at %q: %w", path, err)
	}
	return SaveContent(ctx, store, tag, image...)
}

func SaveOCILayoutNamed(ctx context.Context, path string, tag string, image ...*Image) (ocispec.Descriptor, error) {
	store, err := local.NewStore(path)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to open content store at %q: %w", path, err)
	}
	idx := ociindex.NewStoreIndex(path)

	desc, err := SaveContent(ctx, store, tag, image...)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to save ctrd image: %w", err)
	}

	err = idx.Put(desc, tag)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to update index: %w", err)
	}

	return desc, nil
}

func DeleteOCILayoutNamed(path string, tag string) error {
	if err := ociindex.NewStoreIndex(path).Delete(tag); err != nil {
		return fmt.Errorf("layout %q: %w", path, err)
	}
	return nil
}
