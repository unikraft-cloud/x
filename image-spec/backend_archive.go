// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !js

package imagespec

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images/archive"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// This files provides functions for loading and saving images to/from tarball files using
// containerd's archive package.
//
// This is convenient for moving images around, as well as an intermediate
// format for building images before pushing them.

// LoadTarball loads an image from a tarball (OCI or Docker format), from desc
// if it is set, or else from the tarball's own index.json.
func LoadTarball(ctx context.Context, tarballPath string, desc ocispec.Descriptor, platform platforms.MatchComparer) (*Image, error) {
	f, err := os.Open(tarballPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open tarball: %w", err)
	}
	defer f.Close()

	return LoadTarballReader(ctx, f, desc, platform)
}

// LoadAllTarballs loads all images from a tarball (OCI or Docker format), from
// desc if it is set, or else from the tarball's own index.json.
func LoadAllTarballs(ctx context.Context, tarballPath string, desc ocispec.Descriptor, platform platforms.MatchComparer) ([]*Image, error) {
	f, err := os.Open(tarballPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open tarball: %w", err)
	}
	defer f.Close()

	return LoadAllTarballReaders(ctx, f, desc, platform)
}

// LoadTarballReader loads an image from a tarball reader (OCI or Docker
// format), from desc if it is set, or else from the tarball's own index.json.
func LoadTarballReader(ctx context.Context, r io.Reader, desc ocispec.Descriptor, platform platforms.MatchComparer) (*Image, error) {
	store, root, cleanup, err := importTarball(ctx, r, desc)
	if err != nil {
		return nil, err
	}

	img, err := LoadContent(ctx, store, root, platform)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("failed to load image from imported tarball: %w", err), cleanup())
	}
	img.cleanup = append(img.cleanup, cleanup)
	return img, nil
}

// LoadAllTarballReaders loads all images from a tarball reader (OCI or Docker
// format), from desc if it is set, or else from the tarball's own index.json.
func LoadAllTarballReaders(ctx context.Context, r io.Reader, desc ocispec.Descriptor, platform platforms.MatchComparer) ([]*Image, error) {
	store, root, cleanup, err := importTarball(ctx, r, desc)
	if err != nil {
		return nil, err
	}

	imgs, err := LoadAllContent(ctx, store, root, platform)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("failed to load images from imported tarball: %w", err), cleanup())
	}
	for _, img := range imgs {
		img.cleanup = append(img.cleanup, cleanup)
	}
	return imgs, nil
}

// importTarball imports the tarball r into a temporary store, which cleanup
// removes, and returns the root to load from: desc if it is set, or else the
// tarball's own index.json.
func importTarball(ctx context.Context, r io.Reader, desc ocispec.Descriptor) (_ content.Store, _ ocispec.Descriptor, _ func() error, rerr error) {
	tmpDir, err := os.MkdirTemp("", "imagespec-tarball-*")
	if err != nil {
		return nil, ocispec.Descriptor{}, nil, fmt.Errorf("failed to create temp directory: %w", err)
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
		return nil, ocispec.Descriptor{}, nil, fmt.Errorf("failed to create local store: %w", err)
	}

	root, err := archive.ImportIndex(ctx, store, r)
	if err != nil {
		return nil, ocispec.Descriptor{}, nil, fmt.Errorf("failed to import tarball: %w", err)
	}
	if desc.Digest != "" {
		if _, err := store.Info(ctx, desc.Digest); err != nil {
			return nil, ocispec.Descriptor{}, nil, fmt.Errorf("tarball does not contain %s: %w", desc.Digest, err)
		}
		root = desc
	}
	return store, root, cleanup, nil
}

// SaveTarball saves an image to a tarball file.
func SaveTarball(ctx context.Context, tarballPath string, image ...*Image) (ocispec.Descriptor, error) {
	f, err := os.Create(tarballPath)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to create tarball file: %w", err)
	}
	defer f.Close()

	return SaveTarballWriter(ctx, f, image...)
}

// SaveTarballWriter saves an image to a tarball writer under one index, and
// returns the descriptor of the tarball's own index.json, which lists that
// index: it is what loading the tarball reads from.
func SaveTarballWriter(ctx context.Context, w io.Writer, image ...*Image) (ocispec.Descriptor, error) {
	tmpDir, err := os.MkdirTemp("", "imagespec-tarball-*")
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := local.NewStore(tmpDir)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to create local store: %w", err)
	}

	desc, err := SaveContent(ctx, store, "latest", image...)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to save image to local store: %w", err)
	}

	// Export requires content.InfoReaderProvider which local.Store implements
	infoStore, ok := store.(content.InfoReaderProvider)
	if !ok {
		return ocispec.Descriptor{}, fmt.Errorf("local store does not implement content.InfoReaderProvider")
	}

	pr, pw := io.Pipe()
	index := make(chan ocispec.Descriptor, 1)
	go func() {
		desc, err := tarIndex(pr)
		_ = pr.CloseWithError(err)
		index <- desc
	}()
	err = archive.Export(ctx, infoStore, io.MultiWriter(w, pw), archive.WithManifest(desc))
	_ = pw.CloseWithError(err)
	root := <-index
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to export tarball: %w", err)
	}
	if root.Digest == "" {
		return ocispec.Descriptor{}, fmt.Errorf("exported tarball has no %s", ocispec.ImageIndexFile)
	}
	return root, nil
}

// tarIndex reads the tarball r to its end, and describes its index.json as
// archive.ImportIndex does.
func tarIndex(r io.Reader) (ocispec.Descriptor, error) {
	var desc ocispec.Descriptor
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ocispec.Descriptor{}, err
		}
		if path.Clean(hdr.Name) != ocispec.ImageIndexFile {
			continue
		}
		digester := digest.Canonical.Digester()
		size, err := io.Copy(digester.Hash(), tr)
		if err != nil {
			return ocispec.Descriptor{}, err
		}
		desc = ocispec.Descriptor{MediaType: ocispec.MediaTypeImageIndex, Digest: digester.Digest(), Size: size}
	}
	_, err := io.Copy(io.Discard, r)
	return desc, err
}
