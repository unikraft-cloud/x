// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

// writeBlob writes data to the layout at dir, and returns its descriptor of mediaType.
func writeBlob(t *testing.T, dir, mediaType string, data []byte) ocispec.Descriptor {
	t.Helper()

	desc := ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(data), Size: int64(len(data))}
	path := filepath.Join(dir, ocispec.ImageBlobsDir, desc.Digest.Algorithm().String(), desc.Digest.Encoded())
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return desc
}

// writeJSON writes v to the layout at dir as a blob of mediaType.
func writeJSON(t *testing.T, dir, mediaType string, v any) ocispec.Descriptor {
	t.Helper()

	data, err := json.Marshal(v)
	require.NoError(t, err)
	return writeBlob(t, dir, mediaType, data)
}

// layoutIndex reads the index.json of the layout at dir.
func layoutIndex(t *testing.T, dir string) ocispec.Index {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dir, ocispec.ImageIndexFile))
	require.NoError(t, err)
	var idx ocispec.Index
	require.NoError(t, json.Unmarshal(data, &idx))
	return idx
}

func TestCopyKeepsLayers(t *testing.T) {
	// A linux image, whose layer is no Unikraft part.
	src := t.TempDir()
	layer := writeBlob(t, src, ocispec.MediaTypeImageLayerGzip, []byte("a layer of files"))
	config := writeJSON(t, src, ocispec.MediaTypeImageConfig, ocispec.Image{Architecture: "amd64", OS: "linux"})
	mfst := writeJSON(t, src, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest, Config: config, Layers: []ocispec.Descriptor{layer},
	})
	tagged := mfst
	tagged.Annotations = map[string]string{ocispec.AnnotationRefName: "v1"}
	require.NoError(t, os.WriteFile(filepath.Join(src, ocispec.ImageLayoutFile), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644))
	idx, err := json.Marshal(ocispec.Index{SchemaVersion: 2, MediaType: ocispec.MediaTypeImageIndex, Manifests: []ocispec.Descriptor{tagged}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(src, ocispec.ImageIndexFile), idx, 0o644))

	dst := t.TempDir()
	cmd := CopyCmd{Source: src + ":v1", Destination: dst + ":v2", Insecure: "none"}
	require.NoError(t, cmd.Run(t.Context()))

	copied := layoutIndex(t, dst)
	require.Len(t, copied.Manifests, 1)
	require.Equal(t, mfst.Digest, copied.Manifests[0].Digest)
	require.Equal(t, "v2", copied.Manifests[0].Annotations[ocispec.AnnotationRefName])
	require.FileExists(t, filepath.Join(dst, ocispec.ImageBlobsDir, "sha256", layer.Digest.Encoded()))
}
