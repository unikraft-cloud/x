// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	"github.com/gofrs/flock"
	"github.com/moby/buildkit/client/ociindex"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

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

	err = idx.Put(desc, ociindex.Tag(tag))
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to update index: %w", err)
	}

	return desc, nil
}

func DeleteOCILayoutNamed(path string, tag string) error {
	if tag == "" {
		return fmt.Errorf("empty tag for OCI layout delete")
	}

	// HACK: ociindex doesn't support deleting manifests, so we need to
	// manually read, modify, and write the index. We take the same lock that
	// ociindex uses to ensure consistency with other operations.
	indexPath := filepath.Join(path, ocispec.ImageIndexFile)
	lockPath := indexPath + ".lock"
	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		return fmt.Errorf("could not lock %s: %w", lockPath, err)
	}
	if !locked {
		return fmt.Errorf("could not lock %s", lockPath)
	}
	defer func() {
		_ = lock.Unlock()
		os.RemoveAll(lockPath)
	}()

	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		return fmt.Errorf("failed to read OCI index at %q: %w", path, err)
	}
	var index ocispec.Index
	if err := json.Unmarshal(indexData, &index); err != nil {
		return fmt.Errorf("failed to unmarshal OCI index at %q: %w", path, err)
	}

	originalLen := len(index.Manifests)
	index.Manifests = slices.DeleteFunc(index.Manifests, func(manifest ocispec.Descriptor) bool {
		return matchesOCITag(manifest, tag)
	})
	if len(index.Manifests) == originalLen {
		return fmt.Errorf("tag %q not found in OCI layout %q: %w", tag, path, errdefs.ErrNotFound)
	}
	if index.SchemaVersion == 0 {
		index.SchemaVersion = 2
	}
	if index.MediaType == "" {
		index.MediaType = ocispec.MediaTypeImageIndex
	}

	indexData, err = json.Marshal(index)
	if err != nil {
		return fmt.Errorf("failed to marshal OCI index: %w", err)
	}
	if err := os.WriteFile(indexPath, indexData, 0o644); err != nil {
		return fmt.Errorf("failed to write OCI index %q: %w", indexPath, err)
	}
	return nil
}

func matchesOCITag(desc ocispec.Descriptor, tag string) bool {
	if desc.Annotations == nil {
		return false
	}
	if desc.Annotations[ocispec.AnnotationRefName] == tag {
		return true
	}
	if desc.Annotations[ociContainerdImageNameAnnotation] == tag {
		return true
	}
	return false
}

const ociContainerdImageNameAnnotation = "io.containerd.image.name"
