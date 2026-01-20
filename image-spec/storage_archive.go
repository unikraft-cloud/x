// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images/archive"
	"github.com/containerd/containerd/v2/plugins/content/local"
)

// LoadTarball loads an image from a tarball (OCI or Docker format).
func LoadTarball(ctx context.Context, tarballPath string) (*Image, error) {
	f, err := os.Open(tarballPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open tarball: %w", err)
	}
	defer f.Close()

	return LoadTarballReader(ctx, f)
}

// LoadTarballReader loads an image from a tarball reader (OCI or Docker format).
func LoadTarballReader(ctx context.Context, r io.Reader) (_ *Image, rerr error) {
	tmpDir, err := os.MkdirTemp("", "imagespec-tarball-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	cleanup := func() error {
		return os.RemoveAll(tmpDir)
	}
	defer func() {
		if rerr != nil {
			_ = cleanup()
		}
	}()

	store, err := local.NewStore(tmpDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create local store: %w", err)
	}

	idxDesc, err := archive.ImportIndex(ctx, store, r)
	if err != nil {
		return nil, fmt.Errorf("failed to import tarball: %w", err)
	}

	img, err := loadCtrdImage(ctx, store, idxDesc)
	if err != nil {
		return nil, fmt.Errorf("failed to load image from imported tarball: %w", err)
	}
	img.cleanup = append(img.cleanup, cleanup)
	return img, nil
}

// SaveTarball saves an image to a tarball file.
func SaveTarball(ctx context.Context, tarballPath string, image *Image) error {
	f, err := os.Create(tarballPath)
	if err != nil {
		return fmt.Errorf("failed to create tarball file: %w", err)
	}
	defer f.Close()

	return SaveTarballWriter(ctx, f, image)
}

// SaveTarballWriter saves an image to a tarball writer.
func SaveTarballWriter(ctx context.Context, w io.Writer, image *Image) error {
	tmpDir, err := os.MkdirTemp("", "imagespec-tarball-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := local.NewStore(tmpDir)
	if err != nil {
		return fmt.Errorf("failed to create local store: %w", err)
	}

	desc, err := saveCtrdImage(ctx, store, "latest", image)
	if err != nil {
		return fmt.Errorf("failed to save image to local store: %w", err)
	}

	// Export requires content.InfoReaderProvider which local.Store implements
	infoStore, ok := store.(content.InfoReaderProvider)
	if !ok {
		return fmt.Errorf("local store does not implement content.InfoReaderProvider")
	}

	err = archive.Export(ctx, infoStore, w, archive.WithManifest(desc))
	if err != nil {
		return fmt.Errorf("failed to export tarball: %w", err)
	}

	return nil
}
