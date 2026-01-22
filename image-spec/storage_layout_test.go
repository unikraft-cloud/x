// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestBuildImage(t *testing.T) {
	ctx := t.Context()

	image := NewImage(
		WithKernel(&staticFile{"kernel.img", []byte("kernel data")}),
		WithInitrd(&staticFile{"initrd.img", []byte("initrd data")}),
		WithRom(&staticFile{"rom.img", []byte("rom data")}),
		WithImageConfig(ocispec.ImageConfig{
			Cmd: []string{"example", "--flag"},
		}),
	)

	contentDir := t.TempDir()

	desc, err := SaveOCILayout(ctx, contentDir, "test-image", image)
	require.NoError(t, err)

	store, err := local.NewStore(contentDir)
	require.NoError(t, err)

	mfst, err := images.Manifest(ctx, store, desc, platforms.All)
	require.NoError(t, err)
	require.Len(t, mfst.Layers, 3)

	configBlob, err := content.ReadBlob(ctx, store, mfst.Config)
	require.NoError(t, err)
	var config ocispec.Image
	require.NoError(t, json.Unmarshal(configBlob, &config))
	require.Equal(t, []string{"example", "--flag"}, config.Config.Cmd)
	require.Equal(t, "layers", config.RootFS.Type)
	require.Len(t, config.RootFS.DiffIDs, len(mfst.Layers))

	var sawKernel bool
	var sawInitrd bool
	var sawRom bool
	for i, layer := range mfst.Layers {
		blob, err := content.ReadBlob(ctx, store, layer)
		require.NoError(t, err)

		layerDigest := digest.FromBytes(blob)
		require.Equal(t, layer.Digest, layerDigest)
		require.Equal(t, config.RootFS.DiffIDs[i], layerDigest)

		switch {
		case layer.Annotations[AnnotationKernelPath] == WellKnownKernelPath:
			require.Equal(t, ocispec.MediaTypeImageLayer, layer.MediaType)
			requireTarPayload(t, blob, WellKnownKernelPath, []byte("kernel data"))
			sawKernel = true
		case layer.Annotations[AnnotationKernelInitrdPath] == WellKnownInitrdPath:
			require.Equal(t, ocispec.MediaTypeImageLayer, layer.MediaType)
			requireTarPayload(t, blob, WellKnownInitrdPath, []byte("initrd data"))
			sawInitrd = true
		case layer.MediaType == MediaTypeRom:
			require.Equal(t, []byte("rom data"), blob)
			sawRom = true
		default:
			t.Fatalf("unexpected layer media type %q", layer.MediaType)
		}
	}

	require.True(t, sawKernel)
	require.True(t, sawInitrd)
	require.True(t, sawRom)
}

func requireTarPayload(t *testing.T, blob []byte, target string, expected []byte) {
	t.Helper()

	tr := tar.NewReader(bytes.NewReader(blob))
	_, rr, err := readTarFile(tr, target)
	require.NoError(t, err)

	data, err := io.ReadAll(rr)
	require.NoError(t, err)
	require.Equal(t, expected, data)
}
