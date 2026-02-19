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
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	imagesarchive "github.com/containerd/containerd/v2/core/images/archive"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

var testImage = NewImage(
	WithKernel(&staticFile{"kernel.img", []byte("kernel data")}),
	WithInitrd(&staticFile{"initrd.img", []byte("initrd data")}),
	WithRom(&staticFile{"rom.img", []byte("rom data")}),
	WithImageConfig(ocispec.ImageConfig{
		Cmd: []string{"example", "--flag"},
	}),
)

func TestBuildImage(t *testing.T) {
	ctx := t.Context()

	image := testImage

	tests := []struct {
		name   string
		export func(t *testing.T) (content.Provider, ocispec.Descriptor)
	}{
		{
			name: "oci-layout",
			export: func(t *testing.T) (content.Provider, ocispec.Descriptor) {
				t.Helper()
				contentDir := t.TempDir()
				desc, err := SaveOCILayout(ctx, contentDir, "test-image", image)
				require.NoError(t, err)

				store, err := local.NewStore(contentDir)
				require.NoError(t, err)
				return store, desc
			},
		},
		{
			name: "oci-tar",
			export: func(t *testing.T) (content.Provider, ocispec.Descriptor) {
				t.Helper()
				workDir := t.TempDir()
				tarPath := filepath.Join(workDir, "out.tar")
				require.NoError(t, SaveTarball(ctx, tarPath, image))

				f, err := os.Open(tarPath)
				require.NoError(t, err)
				defer f.Close()

				storeDir := filepath.Join(workDir, "store")
				store, err := local.NewStore(storeDir)
				require.NoError(t, err)

				idxDesc, err := imagesarchive.ImportIndex(ctx, store, f)
				require.NoError(t, err)
				return store, idxDesc
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, desc := test.export(t)

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
		})
	}
}

func TestBuildImageMultiPlatform(t *testing.T) {
	ctx := t.Context()

	tests := []struct {
		name      string
		platforms []ocispec.Platform
	}{
		{
			name: "qemu-fc",
			platforms: []ocispec.Platform{
				{Architecture: "x86_64", OS: "qemu"},
				{Architecture: "x86_64", OS: "fc"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			images := make([]*Image, 0, len(test.platforms))
			for _, platform := range test.platforms {
				img := *testImage
				if img.Image == nil {
					img.Image = &ocispec.Image{}
				} else {
					config := *img.Image
					img.Image = &config
				}
				img.Image.Platform = platform
				images = append(images, &img)
			}

			workDir := t.TempDir()
			storeDir := filepath.Join(workDir, "store")
			store, err := local.NewStore(storeDir)
			require.NoError(t, err)

			_, err = packageLayer(ctx, ingestDefaults(store, content.WithRef("latest")), images[0], images[0].Initrd, ocispec.MediaTypeImageLayer, WellKnownInitrdPath)
			require.NoError(t, err)

			idxDesc, err := SaveContent(ctx, store, "latest", images...)
			require.NoError(t, err)

			idxBlob, err := content.ReadBlob(ctx, store, idxDesc)
			require.NoError(t, err)
			var idx ocispec.Index
			require.NoError(t, json.Unmarshal(idxBlob, &idx))
			require.Len(t, idx.Manifests, len(images))

			for _, mfstDesc := range idx.Manifests {
				mfstBlob, err := content.ReadBlob(ctx, store, mfstDesc)
				require.NoError(t, err)
				var mfst ocispec.Manifest
				require.NoError(t, json.Unmarshal(mfstBlob, &mfst))

				var sawInitrd bool
				for _, layer := range mfst.Layers {
					if layer.Annotations[AnnotationKernelInitrdPath] == WellKnownInitrdPath {
						require.NotEmpty(t, layer.MediaType)
						require.NotEmpty(t, layer.Digest)
						sawInitrd = true
						break
					}
				}
				require.True(t, sawInitrd)
			}
		})
	}
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
