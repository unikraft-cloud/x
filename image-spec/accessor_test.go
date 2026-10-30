// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"archive/tar"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/image-spec/schemes"
)

func platformImages(ps ...ocispec.Platform) []*Image {
	imgs := make([]*Image, 0, len(ps))
	for _, p := range ps {
		img := *testImage
		config := *img.Image
		config.Platform = p
		img.Image = &config
		imgs = append(imgs, &img)
	}
	return imgs
}

func TestSaveLoadRoundTrip(t *testing.T) {
	fcIntel := ocispec.Platform{OS: "fc", Architecture: "x86_64"}
	fcArm := ocispec.Platform{OS: "fc", Architecture: "arm64"}
	qemuIntel := ocispec.Platform{OS: "qemu", Architecture: "x86_64"}

	accessor := NewAccessor()

	for _, tc := range []struct {
		name string
		dest func(t *testing.T) Location
		// wantGone is the error loading the first save gives once the
		// destination has been overwritten, or empty if it still loads.
		wantGone string
	}{
		{
			name: "layout",
			dest: func(t *testing.T) Location { return Location{Scheme: schemes.OCILayout, Path: t.TempDir() + ":v1"} },
		},
		{
			name: "archive",
			dest: func(t *testing.T) Location {
				return Location{Scheme: schemes.OCIArchive, Path: filepath.Join(t.TempDir(), "app.tar")}
			},
			wantGone: "tarball does not contain",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			dest := tc.dest(t)

			saved, err := accessor.Save(ctx, &dest, platformImages(fcIntel, fcArm)...)
			require.NoError(t, err)
			require.NotEmpty(t, saved.Descriptor.Digest)

			imgs, err := accessor.LoadAll(ctx, saved, platforms.All)
			require.NoError(t, err)
			require.Len(t, imgs, 2)
			for _, img := range imgs {
				assert.Equal(t, saved, img.Resolved)
				assert.NotEqual(t, saved.Descriptor, img.ManifestDescriptor)
				require.NoError(t, img.Close())
			}

			img, err := accessor.Load(ctx, saved, platforms.Only(fcArm))
			require.NoError(t, err)
			assert.Equal(t, fcArm, img.Image.Platform)
			require.NoError(t, img.Close())

			_, err = accessor.Save(ctx, &dest, platformImages(qemuIntel)...)
			require.NoError(t, err)

			imgs, err = accessor.LoadAll(ctx, saved, platforms.All)
			if tc.wantGone != "" {
				require.ErrorContains(t, err, tc.wantGone)
				return
			}
			require.NoError(t, err)
			require.Len(t, imgs, 2, "the first save is still what saved names")
			for _, img := range imgs {
				require.NoError(t, img.Close())
			}
		})
	}
}

func TestLoadUnpinned(t *testing.T) {
	ctx := t.Context()
	fcIntel := ocispec.Platform{OS: "fc", Architecture: "x86_64"}
	qemuIntel := ocispec.Platform{OS: "qemu", Architecture: "x86_64"}

	accessor := NewAccessor()

	t.Run("layout", func(t *testing.T) {
		dest := Location{Scheme: schemes.OCILayout, Path: t.TempDir() + ":v1"}
		_, err := accessor.Save(ctx, &dest, platformImages(fcIntel)...)
		require.NoError(t, err)
		_, err = accessor.Save(ctx, &dest, platformImages(qemuIntel)...)
		require.NoError(t, err)

		img, err := accessor.Load(ctx, Resolved{Location: dest}, platforms.All)
		require.NoError(t, err)
		assert.Equal(t, qemuIntel, img.Image.Platform, "an unpinned layout follows its tag")
		require.NoError(t, img.Close())

		path, _ := splitPathTag(dest.Path)
		data, err := os.ReadFile(filepath.Join(path, ocispec.ImageIndexFile))
		require.NoError(t, err)
		var index ocispec.Index
		require.NoError(t, json.Unmarshal(data, &index))
		require.Len(t, index.Manifests, 1)
		assert.Equal(t, index.Manifests[0].Digest, img.Descriptor.Digest, "an unpinned layout is pinned to what its tag names")
	})

	t.Run("archive", func(t *testing.T) {
		dest := Location{Scheme: schemes.OCIArchive, Path: filepath.Join(t.TempDir(), "app.tar")}
		saved, err := accessor.Save(ctx, &dest, platformImages(fcIntel)...)
		require.NoError(t, err)

		img, err := accessor.Load(ctx, Resolved{Location: dest}, platforms.All)
		require.NoError(t, err)
		require.NoError(t, img.Close())
		assert.Equal(t, saved, img.Resolved)

		f, err := os.Open(dest.Path)
		require.NoError(t, err)
		defer f.Close()
		_, r, err := readTarFile(tar.NewReader(f), "index.json")
		require.NoError(t, err)
		index, err := digest.FromReader(r)
		require.NoError(t, err)
		assert.Equal(t, index, img.Descriptor.Digest, "an unpinned archive is pinned to its own index")

		again, err := accessor.Load(ctx, img.Resolved, platforms.All)
		require.NoError(t, err)
		assert.Equal(t, img.Resolved, again.Resolved)
		require.NoError(t, again.Close())
	})
}
