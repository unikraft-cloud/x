// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !js

package imagespec

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestLoadAllTarballClose(t *testing.T) {
	ctx := t.Context()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	tarPath := filepath.Join(t.TempDir(), "out.tar")
	_, err := SaveTarball(ctx, tarPath,
		NewImage(WithKernel(&staticFile{"kernel.img", []byte("x86_64 kernel")}),
			WithPlatform(ocispec.Platform{OS: "kraftcloud", Architecture: "x86_64"})),
		NewImage(WithKernel(&staticFile{"kernel.img", []byte("arm64 kernel")}),
			WithPlatform(ocispec.Platform{OS: "kraftcloud", Architecture: "arm64"})),
	)
	require.NoError(t, err)

	imgs, err := LoadAllTarballs(ctx, tarPath, ocispec.Descriptor{}, platforms.All)
	require.NoError(t, err)
	require.Len(t, imgs, 2)

	// The store stays, and the second image still reads its kernel, after the first one is closed.
	require.NoError(t, imgs[0].Close())
	left, err := os.ReadDir(tmp)
	require.NoError(t, err)
	require.NotEmpty(t, left)
	rc, _, err := imgs[1].Kernel.Open(ctx)
	require.NoError(t, err)
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	require.Contains(t, []string{"x86_64 kernel", "arm64 kernel"}, string(data))

	require.NoError(t, imgs[1].Close())
	left, err = os.ReadDir(tmp)
	require.NoError(t, err)
	require.Empty(t, left)
}

func TestTarballIndexOfLargeImage(t *testing.T) {
	ctx := t.Context()
	tarPath := filepath.Join(t.TempDir(), "out.tar")
	_, err := SaveTarballNamed(ctx, tarPath, "v1", NewImage(WithKernel(&staticFile{"kernel.img", make([]byte, 32<<20)})))
	require.NoError(t, err)

	// With no temp directory, the tarball can only be read, not imported.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	_, entries, err := tarballIndex(ctx, tarPath)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.NoError(t, DeleteTarballNamed(ctx, tarPath, "v1"))
	require.NoFileExists(t, tarPath)
}
