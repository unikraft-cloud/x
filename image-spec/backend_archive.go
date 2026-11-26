// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !js

package imagespec

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sync"
	"sync/atomic"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images/archive"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"unikraft.com/x/image-spec/reference"
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

// LoadAllTarballs loads all images from a tarball (OCI or Docker format).
// The images share one temporary store, which stays until all of them are
// closed. It loads them from
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
	store, root, _, cleanup, err := importTarball(ctx, r, desc)
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

// LoadAllTarballReaders loads all images from a tarball reader. The images
// share one temporary store, which stays until all of them are closed. It
// loads them (OCI or Docker
// format), from desc if it is set, or else from the tarball's own index.json.
func LoadAllTarballReaders(ctx context.Context, r io.Reader, desc ocispec.Descriptor, platform platforms.MatchComparer) ([]*Image, error) {
	store, root, _, cleanup, err := importTarball(ctx, r, desc)
	if err != nil {
		return nil, err
	}

	imgs, err := LoadAllContent(ctx, store, root, platform)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("failed to load images from imported tarball: %w", err), cleanup())
	}
	if len(imgs) == 0 {
		return imgs, cleanup()
	}
	for i, release := range sharedCleanup(cleanup, len(imgs)) {
		imgs[i].cleanup = append(imgs[i].cleanup, release)
	}
	return imgs, nil
}

// sharedCleanup returns n functions that each release one user of cleanup, which runs when the last one is released.
func sharedCleanup(cleanup func() error, n int) []func() error {
	var left atomic.Int64
	left.Store(int64(n))
	releases := make([]func() error, n)
	for i := range releases {
		releases[i] = sync.OnceValue(func() error {
			if left.Add(-1) == 0 {
				return cleanup()
			}
			return nil
		})
	}
	return releases
}

// importTarball imports the tarball r into a temporary store, which cleanup
// removes, and returns the root to load from: desc if it is set, or else the
// tarball's own index.json, and whether the root is that index.json.
func importTarball(ctx context.Context, r io.Reader, desc ocispec.Descriptor) (_ content.Store, _ ocispec.Descriptor, own bool, _ func() error, rerr error) {
	tmpDir, err := os.MkdirTemp("", "imagespec-tarball-*")
	if err != nil {
		return nil, ocispec.Descriptor{}, false, nil, fmt.Errorf("failed to create temp directory: %w", err)
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
		return nil, ocispec.Descriptor{}, false, nil, fmt.Errorf("failed to create local store: %w", err)
	}

	root, err := archive.ImportIndex(ctx, store, r)
	if err != nil {
		return nil, ocispec.Descriptor{}, false, nil, fmt.Errorf("failed to import tarball: %w", err)
	}
	if desc.Digest == "" {
		return store, root, true, cleanup, nil
	}
	if _, err := store.Info(ctx, desc.Digest); err != nil {
		return nil, ocispec.Descriptor{}, false, nil, fmt.Errorf("tarball does not contain %s: %w", desc.Digest, err)
	}
	return store, desc, desc.Digest == root.Digest, cleanup, nil
}

// openTarball imports the tarball r for a copy, with its root: desc, or else the only entry of its index.json.
func openTarball(ctx context.Context, r io.Reader, desc ocispec.Descriptor) (content.Provider, ocispec.Descriptor, func() error, error) {
	store, root, own, cleanup, err := importTarball(ctx, r, desc)
	if err != nil {
		return nil, ocispec.Descriptor{}, nil, err
	}
	if !own {
		return store, root, cleanup, nil
	}
	data, err := readVerifiedBlob(ctx, store, root, docker.MaxManifestSize)
	if err != nil {
		return nil, ocispec.Descriptor{}, nil, errors.Join(err, cleanup())
	}
	var idx ocispec.Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, ocispec.Descriptor{}, nil, errors.Join(fmt.Errorf("failed to read %s of tarball: %w", ocispec.ImageIndexFile, err), cleanup())
	}
	// docker save lists an image once for each of its tags.
	digests := map[digest.Digest]bool{}
	for _, m := range idx.Manifests {
		digests[m.Digest] = true
	}
	if len(digests) != 1 {
		return nil, ocispec.Descriptor{}, nil, errors.Join(fmt.Errorf("tarball lists %d images: name the one to copy by its descriptor", len(digests)), cleanup())
	}
	entry := idx.Manifests[0]
	return store, ocispec.Descriptor{MediaType: entry.MediaType, Digest: entry.Digest, Size: entry.Size}, cleanup, nil
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
	return exportTarball(ctx, w, store, desc, nil)
}

// copyToTarball copies root from provider to a tarball, as copyContent does, and returns its own index.json.
func copyToTarball(ctx context.Context, tarballPath string, provider content.Provider, source reference.Reference, root ocispec.Descriptor, platform platforms.MatchComparer) (ocispec.Descriptor, error) {
	tmpDir, err := os.MkdirTemp("", "imagespec-tarball-*")
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := local.NewStore(tmpDir)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to create local store: %w", err)
	}
	ingester := ingestRef(store, "latest")
	root, err = copyContent(ctx, provider, ingester, ingester, source, root, platform)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to copy image to local store: %w", err)
	}

	f, err := os.Create(tarballPath)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to create tarball file: %w", err)
	}
	desc, err := exportTarball(ctx, f, store, root, platform)
	if err = errors.Join(err, f.Close()); err != nil {
		return ocispec.Descriptor{}, errors.Join(err, os.Remove(tarballPath))
	}
	return desc, nil
}

// exportTarball writes root from store to w as a tarball, with the manifests of platform or all, and returns its own index.json.
func exportTarball(ctx context.Context, w io.Writer, store content.InfoReaderProvider, root ocispec.Descriptor, platform platforms.MatchComparer) (ocispec.Descriptor, error) {
	opts := []archive.ExportOpt{archive.WithManifest(root), archive.WithSkipNonDistributableBlobs()}
	if platform != nil {
		opts = append(opts, archive.WithPlatform(platform))
	}

	pr, pw := io.Pipe()
	index := make(chan ocispec.Descriptor, 1)
	go func() {
		desc, err := tarIndex(pr)
		_ = pr.CloseWithError(err)
		index <- desc
	}()
	err := archive.Export(ctx, store, io.MultiWriter(w, pw), opts...)
	_ = pw.CloseWithError(err)
	own := <-index
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to export tarball: %w", err)
	}
	if own.Digest == "" {
		return ocispec.Descriptor{}, fmt.Errorf("exported tarball has no %s", ocispec.ImageIndexFile)
	}
	return own, nil
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
