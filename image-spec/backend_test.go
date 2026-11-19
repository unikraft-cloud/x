// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	imagesarchive "github.com/containerd/containerd/v2/core/images/archive"
	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/platforms"
	"github.com/klauspost/compress/zstd"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/image-spec/schemes"
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
				desc, err := SaveTarball(ctx, tarPath, image)
				require.NoError(t, err)

				f, err := os.Open(tarPath)
				require.NoError(t, err)
				defer f.Close()

				storeDir := filepath.Join(workDir, "store")
				store, err := local.NewStore(storeDir)
				require.NoError(t, err)

				// The tarball carries the index that SaveTarball reports.
				_, err = imagesarchive.ImportIndex(ctx, store, f)
				require.NoError(t, err)
				return store, desc
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
					require.Failf(t, "unexpected layer", "media type %q", layer.MediaType)
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

			_, _, err = packageLayer(ctx, ingestRef(store, "latest"), images[0], images[0].Initrd, ocispec.MediaTypeImageLayer, WellKnownInitrdPath, CompressionNone)
			require.NoError(t, err)

			idxDesc, err := SaveAllContent(ctx, store, "latest", images)
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

			loaded, err := LoadAllContent(ctx, store, idxDesc, platforms.All)
			require.NoError(t, err)
			require.Len(t, loaded, len(idx.Manifests))
			for _, img := range loaded {
				require.Contains(t, idx.Manifests, img.ManifestDescriptor)
				require.NoError(t, img.Close())
			}
		})
	}
}

func TestPackageLayerTar(t *testing.T) {
	for _, size := range []int{0, 5, 512, 513} {
		data := bytes.Repeat([]byte("x"), size)
		var want bytes.Buffer
		tw := tar.NewWriter(&want)
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: "initrd", Mode: 0o644, Size: int64(size)}))
		_, err := tw.Write(data)
		require.NoError(t, err)
		require.NoError(t, tw.Close())

		store, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		desc, _, err := packageLayer(t.Context(), ingestRef(store, "latest"), testImage, &staticFile{"initrd.img", data}, ocispec.MediaTypeImageLayer, "/initrd", CompressionNone)
		require.NoError(t, err)

		got, err := content.ReadBlob(t.Context(), store, desc)
		require.NoError(t, err)
		require.Equal(t, want.Bytes(), got, "a file of %d bytes", size)

		desc, diffID, err := packageLayer(t.Context(), ingestRef(store, "latest"), testImage, &staticFile{"initrd.img", data}, ocispec.MediaTypeImageLayer, "/initrd", CompressionZstd)
		require.NoError(t, err)
		require.Equal(t, ocispec.MediaTypeImageLayerZstd, desc.MediaType)
		require.Equal(t, digest.FromBytes(want.Bytes()), diffID)
		got, err = content.ReadBlob(t.Context(), store, desc)
		require.NoError(t, err)
		zr, err := decompressLayer(desc.MediaType, bytes.NewReader(got))
		require.NoError(t, err)
		got, err = io.ReadAll(zr)
		zr.Close()
		require.NoError(t, err)
		require.Equal(t, want.Bytes(), got, "a compressed file of %d bytes", size)
	}
}

func TestDeleteImage(t *testing.T) {
	ctx := t.Context()

	t.Run("tarball", func(t *testing.T) {
		workDir := t.TempDir()
		tarPath := filepath.Join(workDir, "image.tar")

		// Save an image to a tarball
		_, err := SaveTarball(ctx, tarPath, testImage)
		require.NoError(t, err)

		// Verify the tarball exists
		_, err = os.Stat(tarPath)
		require.NoError(t, err)

		// Delete the tarball
		require.NoError(t, os.Remove(tarPath))

		// Verify the tarball is deleted
		_, err = os.Stat(tarPath)
		require.True(t, os.IsNotExist(err))
	})

	t.Run("oci-layout", func(t *testing.T) {
		contentDir := t.TempDir()

		// Save multiple images with different tags
		_, err := SaveOCILayoutNamed(ctx, contentDir, "tag1", testImage)
		require.NoError(t, err)
		_, err = SaveOCILayoutNamed(ctx, contentDir, "tag2", testImage)
		require.NoError(t, err)
		_, err = SaveOCILayoutNamed(ctx, contentDir, "tag3", testImage)
		require.NoError(t, err)

		// Verify all tags exist in the index
		indexPath := filepath.Join(contentDir, ocispec.ImageIndexFile)
		indexData, err := os.ReadFile(indexPath)
		require.NoError(t, err)
		var index ocispec.Index
		require.NoError(t, json.Unmarshal(indexData, &index))
		require.Len(t, index.Manifests, 3)

		// Helper to check tags in index
		hasTag := func(index *ocispec.Index, tag string) bool {
			for _, m := range index.Manifests {
				if m.Annotations[ocispec.AnnotationRefName] == tag {
					return true
				}
			}
			return false
		}

		require.True(t, hasTag(&index, "tag1"))
		require.True(t, hasTag(&index, "tag2"))
		require.True(t, hasTag(&index, "tag3"))

		// Delete tag2
		require.NoError(t, DeleteOCILayoutNamed(contentDir, "tag2"))

		// Verify tag2 is removed but tag1 and tag3 remain
		indexData, err = os.ReadFile(indexPath)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(indexData, &index))
		require.Len(t, index.Manifests, 2)
		require.True(t, hasTag(&index, "tag1"))
		require.False(t, hasTag(&index, "tag2"))
		require.True(t, hasTag(&index, "tag3"))

		// Delete tag1
		require.NoError(t, DeleteOCILayoutNamed(contentDir, "tag1"))

		// Verify only tag3 remains
		indexData, err = os.ReadFile(indexPath)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(indexData, &index))
		require.Len(t, index.Manifests, 1)
		require.False(t, hasTag(&index, "tag1"))
		require.False(t, hasTag(&index, "tag2"))
		require.True(t, hasTag(&index, "tag3"))

		// Delete non-existent tag should fail
		err = DeleteOCILayoutNamed(contentDir, "nonexistent")
		require.Error(t, err)
		require.Contains(t, err.Error(), "not found")
	})
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

func TestOSFeaturesOnlyInConfig(t *testing.T) {
	ctx := t.Context()

	platform := ocispec.Platform{Architecture: "x86_64", OS: "fc", OSFeatures: []string{"CONFIG_EROFS_FS=y"}}
	img := *testImage
	img.Image = &ocispec.Image{Platform: platform}

	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	idxDesc, err := SaveContent(ctx, store, "latest", &img)
	require.NoError(t, err)

	idxBlob, err := content.ReadBlob(ctx, store, idxDesc)
	require.NoError(t, err)
	var idx ocispec.Index
	require.NoError(t, json.Unmarshal(idxBlob, &idx))
	require.Len(t, idx.Manifests, 1)
	require.NotNil(t, idx.Manifests[0].Platform)
	require.Empty(t, idx.Manifests[0].Platform.OSFeatures)
	require.Equal(t, "fc", idx.Manifests[0].Platform.OS)

	loaded, err := LoadAllContent(ctx, store, idxDesc, platforms.Any(ocispec.Platform{Architecture: "x86_64", OS: "fc"}))
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, platform.OSFeatures, loaded[0].Image.OSFeatures)
}

func TestIndexAnnotations(t *testing.T) {
	ctx := t.Context()

	annotations := map[string]string{AnnotationBuildID: "0b9c6f1e-6a52-4d1f-9a43-2f0d9b7f5c11"}
	img := *testImage
	img.Image = &ocispec.Image{Architecture: "x86_64", OS: "fc"}
	img.IndexAnnotations = annotations

	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	idxDesc, err := SaveContent(ctx, store, "latest", &img)
	require.NoError(t, err)

	idxBlob, err := content.ReadBlob(ctx, store, idxDesc)
	require.NoError(t, err)
	var idx ocispec.Index
	require.NoError(t, json.Unmarshal(idxBlob, &idx))
	require.Equal(t, annotations, idx.Annotations)

	loaded, err := LoadAllContent(ctx, store, idxDesc, platforms.All)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, annotations, loaded[0].IndexAnnotations)

	resaved, err := SaveAllContent(ctx, store, "latest", loaded)
	require.NoError(t, err)
	require.Equal(t, idxDesc.Digest, resaved.Digest)
}

func TestLoadIgnoresDescriptorOSFeatures(t *testing.T) {
	ctx := t.Context()

	img := *testImage
	img.Image = &ocispec.Image{Architecture: "x86_64", OS: "fc"}

	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	idxDesc, err := SaveContent(ctx, store, "latest", &img)
	require.NoError(t, err)

	idxBlob, err := content.ReadBlob(ctx, store, idxDesc)
	require.NoError(t, err)
	var idx ocispec.Index
	require.NoError(t, json.Unmarshal(idxBlob, &idx))
	idx.Manifests[0].Platform.OSFeatures = []string{"CONFIG_EROFS_FS=y"}
	legacyBlob, err := json.Marshal(idx)
	require.NoError(t, err)
	legacyDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageIndex,
		Digest:    digest.FromBytes(legacyBlob),
		Size:      int64(len(legacyBlob)),
	}
	require.NoError(t, content.WriteBlob(ctx, store, "legacy", bytes.NewReader(legacyBlob), legacyDesc))

	want := ocispec.Platform{Architecture: "x86_64", OS: "fc"}
	for name, matcher := range map[string]platforms.MatchComparer{
		"any":         platforms.Any(want),
		"only-strict": platforms.OnlyStrict(want),
	} {
		t.Run(name, func(t *testing.T) {
			loaded, err := LoadAllContent(ctx, store, legacyDesc, matcher)
			require.NoError(t, err)
			require.Len(t, loaded, 1)

			single, err := LoadContent(ctx, store, legacyDesc, matcher)
			require.NoError(t, err)
			require.NotNil(t, single)
		})
	}
}

func TestSaveByManifest(t *testing.T) {
	ctx := t.Context()

	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	var byManifest []*Image
	for _, arch := range []string{"x86_64", "arm64"} {
		img := *testImage
		img.Image = &ocispec.Image{Architecture: arch, OS: "fc"}
		desc, err := SaveManifest(ctx, store, "tmp-"+arch, &img)
		require.NoError(t, err)
		byManifest = append(byManifest, &Image{ManifestDescriptor: desc})
	}

	idxDesc, err := SaveAllContent(ctx, store, "latest", byManifest)
	require.NoError(t, err)

	loaded, err := LoadAllContent(ctx, store, idxDesc, platforms.All)
	require.NoError(t, err)
	require.Len(t, loaded, 2)
	for i, img := range loaded {
		require.Equal(t, byManifest[i].ManifestDescriptor, img.ManifestDescriptor)
	}
}

// writeJSON writes v to store as a blob of mediaType.
func writeJSON(t *testing.T, store content.Ingester, mediaType string, v any) ocispec.Descriptor {
	data, err := json.Marshal(v)
	require.NoError(t, err)
	desc := ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(data), Size: int64(len(data))}
	require.NoError(t, content.WriteBlob(t.Context(), store, desc.Digest.String(), bytes.NewReader(data), desc))
	return desc
}

// layoutTar returns an OCI layout tarball with index and the blobs from store.
func layoutTar(t *testing.T, store content.Provider, index []byte, blobs ...ocispec.Descriptor) *bytes.Buffer {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(name string, data []byte) {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}))
		_, err := tw.Write(data)
		require.NoError(t, err)
	}
	add(ocispec.ImageLayoutFile, []byte(`{"imageLayoutVersion":"1.0.0"}`))
	add(ocispec.ImageIndexFile, index)
	for _, desc := range blobs {
		data, err := content.ReadBlob(t.Context(), store, desc)
		require.NoError(t, err)
		add("blobs/sha256/"+desc.Digest.Encoded(), data)
	}
	require.NoError(t, tw.Close())
	return &buf
}

func TestSaveCopiesLoadedManifests(t *testing.T) {
	ctx := t.Context()

	src, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	var blobs []ocispec.Descriptor
	var entries []ocispec.Descriptor
	// The entry without a platform comes first, which loading moves last.
	for _, p := range []ocispec.Platform{{Architecture: "unknown", OS: "unknown"}, {Architecture: "amd64", OS: "linux"}} {
		data := []byte("layer of " + p.Architecture)
		layer := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: digest.FromBytes(data), Size: int64(len(data))}
		require.NoError(t, content.WriteBlob(ctx, src, layer.Digest.String(), bytes.NewReader(data), layer))
		config := writeJSON(t, src, ocispec.MediaTypeImageConfig, ocispec.Image{Platform: p})
		mfst := writeJSON(t, src, ocispec.MediaTypeImageManifest, ocispec.Manifest{
			SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest, Config: config, Layers: []ocispec.Descriptor{layer},
		})
		blobs = append(blobs, layer, config, mfst)
		if p.OS != "unknown" {
			mfst.Platform = &p
		}
		entries = append(entries, mfst)
	}
	idxDesc := writeJSON(t, src, ocispec.MediaTypeImageIndex, ocispec.Index{
		SchemaVersion: 2, MediaType: ocispec.MediaTypeImageIndex, Manifests: entries,
	})

	requireBlobs := func(t *testing.T, store content.Store, descs ...ocispec.Descriptor) {
		for _, desc := range descs {
			_, err := store.Info(ctx, desc.Digest)
			require.NoError(t, err, desc.Digest)
		}
	}

	t.Run("all", func(t *testing.T) {
		loaded, err := LoadAllContent(ctx, src, idxDesc, platforms.All)
		require.NoError(t, err)
		require.Len(t, loaded, 2)

		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		saved, err := SaveAllContent(ctx, dst, "latest", loaded)
		require.NoError(t, err)
		require.Equal(t, idxDesc.Digest, saved.Digest)
		requireBlobs(t, dst, append(blobs, idxDesc)...)
	})

	t.Run("index order", func(t *testing.T) {
		// Without a platform, a load keeps the order of the index, here with the entry without a platform first.
		loaded, err := LoadAllContent(ctx, src, idxDesc, nil)
		require.NoError(t, err)
		require.Nil(t, loaded[0].ManifestDescriptor.Platform)

		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		saved, err := SaveAllContent(ctx, dst, "latest", loaded)
		require.NoError(t, err)
		require.Equal(t, idxDesc.Digest, saved.Digest)
	})

	t.Run("order", func(t *testing.T) {
		loaded, err := LoadAllContent(ctx, src, idxDesc, platforms.All)
		require.NoError(t, err)
		slices.Reverse(loaded)

		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		saved, err := SaveAllContent(ctx, dst, "latest", loaded)
		require.NoError(t, err)
		require.NotEqual(t, idxDesc.Digest, saved.Digest)
		data, err := content.ReadBlob(ctx, dst, saved)
		require.NoError(t, err)
		var idx ocispec.Index
		require.NoError(t, json.Unmarshal(data, &idx))
		require.Len(t, idx.Manifests, 2)
		require.Equal(t, loaded[0].ManifestDescriptor.Digest, idx.Manifests[0].Digest)
		require.Equal(t, loaded[1].ManifestDescriptor.Digest, idx.Manifests[1].Digest)
	})

	t.Run("resumed", func(t *testing.T) {
		loaded, err := LoadAllContent(ctx, src, idxDesc, platforms.All)
		require.NoError(t, err)
		layer := blobs[0]
		data, err := content.ReadBlob(ctx, src, layer)
		require.NoError(t, err)

		// An earlier save left half of the layer in the store.
		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		w, err := dst.Writer(ctx, content.WithRef("latest@"+layer.Digest.String()), content.WithDescriptor(layer))
		require.NoError(t, err)
		_, err = w.Write(data[:len(data)/2])
		require.NoError(t, err)
		require.NoError(t, w.Close())

		saved, err := SaveAllContent(ctx, dst, "latest", loaded)
		require.NoError(t, err)
		require.Equal(t, idxDesc.Digest, saved.Digest)
		got, err := content.ReadBlob(ctx, dst, layer)
		require.NoError(t, err)
		require.Equal(t, data, got)
	})

	t.Run("reset", func(t *testing.T) {
		loaded, err := LoadAllContent(ctx, src, idxDesc, platforms.All)
		require.NoError(t, err)

		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		store := &resetting{Ingester: dst}
		saved, err := SaveAllContent(ctx, store, "latest", loaded)
		require.NoError(t, err)
		require.Equal(t, idxDesc.Digest, saved.Digest)
		requireBlobs(t, dst, append(blobs, idxDesc)...)
		require.GreaterOrEqual(t, store.resets.Load(), int32(len(blobs)))
	})

	t.Run("manifests last", func(t *testing.T) {
		loaded, err := LoadAllContent(ctx, src, idxDesc, platforms.All)
		require.NoError(t, err)

		// The layer of the image without a platform, which loads last, fails once the other image could write its manifest.
		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		store := &failingLayer{Ingester: dst, fail: blobs[0].Digest, manifest: make(chan struct{})}
		_, err = SaveAllContent(ctx, store, "latest", loaded)
		require.Error(t, err)
		require.False(t, store.wroteManifest.Load(), "a manifest was written before all blobs")
	})

	t.Run("annotations", func(t *testing.T) {
		loaded, err := LoadAllContent(ctx, src, idxDesc, platforms.All)
		require.NoError(t, err)
		loaded[0].IndexAnnotations = map[string]string{AnnotationBuildID: "changed"}

		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		saved, err := SaveAllContent(ctx, dst, "latest", loaded)
		require.NoError(t, err)
		require.NotEqual(t, idxDesc.Digest, saved.Digest)
	})

	t.Run("descriptor", func(t *testing.T) {
		loaded, err := LoadAllContent(ctx, src, idxDesc, platforms.All)
		require.NoError(t, err)
		loaded[1].ManifestDescriptor.Annotations = map[string]string{AnnotationBuildID: "changed"}

		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		saved, err := SaveAllContent(ctx, dst, "latest", loaded)
		require.NoError(t, err)
		require.NotEqual(t, idxDesc.Digest, saved.Digest)
	})

	t.Run("nested", func(t *testing.T) {
		outer := writeJSON(t, src, ocispec.MediaTypeImageIndex, ocispec.Index{
			SchemaVersion: 2, MediaType: ocispec.MediaTypeImageIndex, Manifests: []ocispec.Descriptor{idxDesc},
		})
		loaded, err := LoadAllContent(ctx, src, outer, platforms.All)
		require.NoError(t, err)
		require.Len(t, loaded, 2)

		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		saved, err := SaveAllContent(ctx, dst, "latest", loaded)
		require.NoError(t, err)
		require.NotEqual(t, idxDesc.Digest, saved.Digest)
		require.NotEqual(t, outer.Digest, saved.Digest)
	})

	t.Run("platform", func(t *testing.T) {
		loaded, err := LoadAllContent(ctx, src, idxDesc, platforms.OnlyStrict(ocispec.Platform{Architecture: "amd64", OS: "linux"}))
		require.NoError(t, err)
		require.Len(t, loaded, 1)

		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		saved, err := SaveAllContent(ctx, dst, "latest", loaded)
		require.NoError(t, err)
		require.NotEqual(t, idxDesc.Digest, saved.Digest)
		requireBlobs(t, dst, blobs[3:]...)

		reloaded, err := LoadAllContent(ctx, dst, saved, platforms.All)
		require.NoError(t, err)
		require.Len(t, reloaded, 1)
		require.Equal(t, entries[1].Digest, reloaded[0].ManifestDescriptor.Digest)
	})

	t.Run("tarball", func(t *testing.T) {
		loaded, err := LoadAllContent(ctx, src, idxDesc, platforms.All)
		require.NoError(t, err)

		var buf bytes.Buffer
		root, err := SaveAllTarballWriters(ctx, &buf, loaded)
		require.NoError(t, err)

		reloaded, err := LoadAllTarballReaders(ctx, &buf, root, platforms.All)
		require.NoError(t, err)
		defer func() {
			for _, img := range reloaded {
				require.NoError(t, img.Close())
			}
		}()
		require.Len(t, reloaded, 2)

		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		saved, err := SaveAllContent(ctx, dst, "latest", reloaded)
		require.NoError(t, err)
		require.Equal(t, idxDesc.Digest, saved.Digest)
		require.Empty(t, saved.Annotations)
		requireBlobs(t, dst, append(blobs, idxDesc)...)
	})

	t.Run("layout manifests", func(t *testing.T) {
		index, err := json.Marshal(ocispec.Index{SchemaVersion: 2, Manifests: entries})
		require.NoError(t, err)
		loaded, err := LoadAllTarballReaders(ctx, layoutTar(t, src, index, blobs...), ocispec.Descriptor{}, platforms.All)
		require.NoError(t, err)
		defer func() {
			for _, img := range loaded {
				require.NoError(t, img.Close())
			}
		}()
		require.Len(t, loaded, 2)

		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		saved, err := SaveAllContent(ctx, dst, "latest", loaded)
		require.NoError(t, err)
		require.NotEqual(t, digest.FromBytes(index), saved.Digest)
		data, err := content.ReadBlob(ctx, dst, saved)
		require.NoError(t, err)
		var idx ocispec.Index
		require.NoError(t, json.Unmarshal(data, &idx))
		require.Equal(t, ocispec.MediaTypeImageIndex, idx.MediaType)
		require.Len(t, idx.Manifests, 2)
		for _, entry := range idx.Manifests {
			require.NotNil(t, entry.Platform, entry.Digest)
		}
		requireBlobs(t, dst, blobs...)
	})
}

// failingLayer is an ingester that fails the write of one blob, after a manifest write or a while.
type failingLayer struct {
	content.Ingester
	fail          digest.Digest
	manifest      chan struct{}
	once          sync.Once
	wroteManifest atomic.Bool
}

func (s *failingLayer) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	var wo content.WriterOpts
	for _, opt := range opts {
		if err := opt(&wo); err != nil {
			return nil, err
		}
	}
	if images.IsManifestType(wo.Desc.MediaType) || images.IsIndexType(wo.Desc.MediaType) {
		s.wroteManifest.Store(true)
		s.once.Do(func() { close(s.manifest) })
	}
	if wo.Desc.Digest == s.fail {
		select {
		case <-s.manifest:
		case <-time.After(500 * time.Millisecond):
		}
		return nil, errors.New("layer upload failed")
	}
	return s.Ingester.Writer(ctx, opts...)
}

func TestSaveCopiesInlineConfig(t *testing.T) {
	ctx := t.Context()

	// The config is only in its descriptor, as the data field of OCI allows.
	src, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	data := []byte("layer")
	layer := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: digest.FromBytes(data), Size: int64(len(data))}
	require.NoError(t, content.WriteBlob(ctx, src, layer.Digest.String(), bytes.NewReader(data), layer))
	raw, err := json.Marshal(ocispec.Image{Architecture: "amd64", OS: "linux"})
	require.NoError(t, err)
	config := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig, Digest: digest.FromBytes(raw), Size: int64(len(raw)), Data: raw}
	mfst := writeJSON(t, src, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest, Config: config, Layers: []ocispec.Descriptor{layer},
	})

	loaded, err := LoadAllContent(ctx, src, mfst, platforms.All)
	require.NoError(t, err)
	dst, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	_, err = SaveAllContent(ctx, dst, "latest", loaded)
	require.NoError(t, err)
	got, err := content.ReadBlob(ctx, dst, ocispec.Descriptor{Digest: config.Digest, Size: config.Size})
	require.NoError(t, err)
	require.Equal(t, raw, got)
}

func TestCopyableBlobsOnce(t *testing.T) {
	config := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig, Digest: digest.FromString("config")}
	layer := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: digest.FromString("layer")}
	blobs, err := copyableBlobs(ocispec.Manifest{Config: config, Layers: []ocispec.Descriptor{layer, layer}})
	require.NoError(t, err)
	require.Equal(t, []ocispec.Descriptor{config, layer}, blobs)
}

func TestCopyableBlobsNoManifests(t *testing.T) {
	config := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig, Digest: digest.FromString("config")}
	layer := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: digest.FromString("layer")}
	for _, mediaType := range []string{ocispec.MediaTypeImageManifest, ocispec.MediaTypeImageIndex, images.MediaTypeDockerSchema2Manifest, images.MediaTypeDockerSchema2ManifestList} {
		other := ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromString(mediaType)}
		_, err := copyableBlobs(ocispec.Manifest{Config: config, Layers: []ocispec.Descriptor{layer, other}})
		require.Error(t, err, mediaType)
		_, err = copyableBlobs(ocispec.Manifest{Config: other, Layers: []ocispec.Descriptor{layer}})
		require.Error(t, err, mediaType)
	}
}

// recording records the descriptors of the writers that a save opens.
type recording struct {
	content.Ingester
	mu    sync.Mutex
	descs []ocispec.Descriptor
}

func (r *recording) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	var o content.WriterOpts
	for _, opt := range opts {
		if err := opt(&o); err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	r.descs = append(r.descs, o.Desc)
	r.mu.Unlock()
	return r.Ingester.Writer(ctx, opts...)
}

func TestSaveDropsSourceLabelsOfManifest(t *testing.T) {
	ctx := t.Context()

	// The manifest asks a pusher to mount the layer from a repository of the destination registry.
	src, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	data := []byte("layer")
	source := labels.LabelDistributionSource + ".index.unikraft.io"
	layer := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayerGzip, Digest: digest.FromBytes(data), Size: int64(len(data)),
		Annotations: map[string]string{source: "victim/private"},
	}
	require.NoError(t, content.WriteBlob(ctx, src, layer.Digest.String(), bytes.NewReader(data), layer))
	platform := ocispec.Platform{Architecture: "amd64", OS: "linux"}
	config := writeJSON(t, src, ocispec.MediaTypeImageConfig, ocispec.Image{Platform: platform})
	mfst := writeJSON(t, src, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest, Config: config, Layers: []ocispec.Descriptor{layer},
	})
	loaded, err := LoadAllContent(ctx, src, mfst, platforms.All)
	require.NoError(t, err)

	dst, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	store := &recording{Ingester: dst}
	_, err = SaveAllContent(ctx, store, "latest", loaded)
	require.NoError(t, err)
	require.NotEmpty(t, store.descs)
	for _, desc := range store.descs {
		require.NotContains(t, desc.Annotations, source, desc.Digest)
	}
}

func TestSaveRestartsResumedWrite(t *testing.T) {
	ctx := t.Context()

	scratch, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	idxDesc, err := SaveContent(ctx, scratch, "latest", testImage)
	require.NoError(t, err)
	mfsts, err := Manifests(ctx, scratch, idxDesc, nil)
	require.NoError(t, err)
	layer := mfsts[0].Layers[0]
	data, err := content.ReadBlob(ctx, scratch, layer)
	require.NoError(t, err)

	// An earlier save left half of the layer in the store.
	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	w, err := store.Writer(ctx, content.WithRef("latest@"+layer.Digest.String()), content.WithDescriptor(layer))
	require.NoError(t, err)
	_, err = w.Write(data[:len(data)/2])
	require.NoError(t, err)
	require.NoError(t, w.Close())

	saved, err := SaveContent(ctx, store, "latest", testImage)
	require.NoError(t, err)
	require.Equal(t, idxDesc.Digest, saved.Digest)
	got, err := content.ReadBlob(ctx, store, layer)
	require.NoError(t, err)
	require.Equal(t, data, got)
}

func TestSaveSkipsForeignLayers(t *testing.T) {
	ctx := t.Context()

	src, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	config := writeJSON(t, src, ocispec.MediaTypeImageConfig, ocispec.Image{Architecture: "amd64", OS: "windows"})
	foreign := ocispec.Descriptor{
		MediaType: images.MediaTypeDockerSchema2LayerForeignGzip,
		Digest:    digest.FromString("foreign"),
		Size:      7,
		URLs:      []string{"https://example.com/foreign"},
	}
	mfst := writeJSON(t, src, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest, Config: config, Layers: []ocispec.Descriptor{foreign},
	})

	loaded, err := LoadAllContent(ctx, src, mfst, platforms.All)
	require.NoError(t, err)

	dst, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	_, err = SaveAllContent(ctx, dst, "latest", loaded)
	require.NoError(t, err)
	for _, desc := range []ocispec.Descriptor{config, mfst} {
		_, err := dst.Info(ctx, desc.Digest)
		require.NoError(t, err)
	}
	_, err = dst.Info(ctx, foreign.Digest)
	require.Error(t, err)

	var buf bytes.Buffer
	_, err = SaveAllTarballWriters(ctx, &buf, loaded)
	require.NoError(t, err)
}

func TestDecompressLayerWindow(t *testing.T) {
	// A zstd frame of one raw byte, whose header asks for a window of 2^(10+exp) bytes.
	frame := func(exp byte) []byte {
		return []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, exp << 3, 0x09, 0x00, 0x00, 'a'}
	}
	for _, tc := range []struct {
		exp byte
		ok  bool
	}{{13, true}, {17, true}, {18, false}} {
		r, err := decompressLayer(ocispec.MediaTypeImageLayerZstd, bytes.NewReader(frame(tc.exp)))
		require.NoError(t, err)
		data, err := io.ReadAll(r)
		r.Close()
		if tc.ok {
			require.NoError(t, err, "window 2^%d", 10+tc.exp)
			require.Equal(t, "a", string(data))
		} else {
			require.ErrorIs(t, err, zstd.ErrWindowSizeExceeded, "window 2^%d", 10+tc.exp)
		}
	}
}

func TestSaveCompressedParts(t *testing.T) {
	ctx := t.Context()
	kernel, initrd := bytes.Repeat([]byte("kernel "), 512), bytes.Repeat([]byte("initrd "), 4096)
	newImage := func() *Image {
		return NewImage(
			WithKernel(&staticFile{"kernel.img", kernel}),
			WithInitrd(&staticFile{"initrd.img", initrd}),
			WithRom(&staticFile{"rom.img", []byte("rom data")}),
			WithImageConfig(ocispec.ImageConfig{Cmd: []string{"example"}}),
		)
	}
	save := func(dir string, opts ...SaveOpt) ocispec.Descriptor {
		saved, err := NewAccessor().Save(ctx, &Location{Scheme: schemes.OCILayout, Path: dir + ":latest"}, newImage(), opts...)
		require.NoError(t, err)
		return saved.Descriptor
	}

	_, err := NewAccessor().Save(ctx, &Location{Scheme: schemes.OCILayout, Path: t.TempDir() + ":latest"}, newImage(), WithCompression("gzip"))
	require.ErrorContains(t, err, `unknown compression "gzip"`)

	plainDir := t.TempDir()
	plainDesc := save(plainDir)
	plain, err := local.NewStore(plainDir)
	require.NoError(t, err)
	plainMfsts, err := Manifests(ctx, plain, plainDesc, nil)
	require.NoError(t, err)
	for _, layer := range plainMfsts[0].Layers[:2] {
		require.Equal(t, ocispec.MediaTypeImageLayer, layer.MediaType, "a save without compression writes plain tar layers")
	}

	srcDir := t.TempDir()
	idxDesc := save(srcDir, WithCompression(CompressionZstd))
	src, err := local.NewStore(srcDir)
	require.NoError(t, err)

	mfsts, err := Manifests(ctx, src, idxDesc, nil)
	require.NoError(t, err)
	mfst := mfsts[0]
	require.Len(t, mfst.Layers, 3)
	require.Equal(t, ocispec.MediaTypeImageLayerZstd, mfst.Layers[0].MediaType)
	require.Equal(t, ocispec.MediaTypeImageLayerZstd, mfst.Layers[1].MediaType)
	require.Equal(t, MediaTypeRom, mfst.Layers[2].MediaType)
	config, err := content.ReadBlob(ctx, src, mfst.Config)
	require.NoError(t, err)
	var img ocispec.Image
	require.NoError(t, json.Unmarshal(config, &img))
	for i, layer := range mfst.Layers[:2] {
		blob, err := content.ReadBlob(ctx, src, layer)
		require.NoError(t, err)
		r, err := decompressLayer(layer.MediaType, bytes.NewReader(blob))
		require.NoError(t, err)
		tarball, err := io.ReadAll(r)
		require.NoError(t, err)
		require.Equal(t, digest.FromBytes(tarball), img.RootFS.DiffIDs[i])
	}
	require.Equal(t, mfst.Layers[2].Digest, img.RootFS.DiffIDs[2])

	require.Equal(t, idxDesc.Digest, save(t.TempDir(), WithCompression(CompressionZstd)).Digest, "the same image compresses to the same blobs")

	loaded, err := LoadAllContent(ctx, src, idxDesc, platforms.All)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	for want, file := range map[string]File{string(kernel): loaded[0].Kernel, string(initrd): loaded[0].Initrd} {
		require.NotNil(t, file)
		r, _, err := file.Open(ctx)
		require.NoError(t, err)
		got, err := io.ReadAll(r)
		require.NoError(t, err)
		require.NoError(t, r.Close())
		require.Equal(t, want, string(got))
	}

	dst, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	saved, err := SaveAllContent(ctx, dst, "latest", loaded)
	require.NoError(t, err)
	require.Equal(t, idxDesc.Digest, saved.Digest)

	// A part moves to another image with its DiffID, or the save computes it.
	for name, part := range map[string]File{
		"loaded": loaded[0].Initrd,
		"bare":   NewContentStoreFile(src, mfst.Layers[1], WellKnownInitrdPath),
	} {
		moved, err := SaveContent(ctx, dst, name, NewImage(WithInitrd(part)))
		require.NoError(t, err)
		mfsts, err := Manifests(ctx, dst, moved, nil)
		require.NoError(t, err)
		config, err := content.ReadBlob(ctx, dst, mfsts[0].Config)
		require.NoError(t, err)
		var movedImg ocispec.Image
		require.NoError(t, json.Unmarshal(config, &movedImg))
		require.Equal(t, mfst.Layers[1].Digest, mfsts[0].Layers[0].Digest, name)
		require.Equal(t, img.RootFS.DiffIDs[1], movedImg.RootFS.DiffIDs[0], name)
	}
}

type tarEntry struct {
	name string
	data []byte
}

// zstdTarLayer writes a zstd tar layer of the entries to store.
func zstdTarLayer(t *testing.T, store content.Ingester, entries ...tarEntry) ocispec.Descriptor {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	require.NoError(t, err)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.data))}))
		_, err := tw.Write(e.data)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())
	desc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayerZstd, Digest: digest.FromBytes(buf.Bytes()), Size: int64(buf.Len())}
	require.NoError(t, content.WriteBlob(t.Context(), store, desc.Digest.String(), bytes.NewReader(buf.Bytes()), desc))
	return desc
}

func TestLayerDiffIDLimit(t *testing.T) {
	t.Parallel()

	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	desc := zstdTarLayer(t, store, tarEntry{"initrd", make([]byte, 2<<20)})

	_, err = layerDiffIDWithin(t.Context(), nil, store, desc, 1<<20)
	require.ErrorIs(t, err, ErrLayerTooLarge)
	id, err := layerDiffIDWithin(t.Context(), nil, store, desc, 4<<20)
	require.NoError(t, err)
	require.NotEqual(t, desc.Digest, id)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = layerDiffIDWithin(ctx, nil, store, desc, 4<<20)
	require.ErrorIs(t, err, context.Canceled)
}

func TestContentStoreFileLimit(t *testing.T) {
	t.Parallel()

	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	// An entry before the kernel decompresses past the limit.
	desc := zstdTarLayer(t, store, tarEntry{"junk", make([]byte, 2<<20)}, tarEntry{"kernel", []byte("kernel")})

	_, _, err = (&ContentStoreFile{Store: store, Desc: desc, Path_: "/kernel", maxSize: 1 << 20}).Open(t.Context())
	require.ErrorIs(t, err, ErrLayerTooLarge)

	r, size, err := NewContentStoreFile(store, desc, "/kernel").Open(t.Context())
	require.NoError(t, err)
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	require.Equal(t, int64(6), size)
	require.Equal(t, "kernel", string(data))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = NewContentStoreFile(store, desc, "/kernel").Open(ctx)
	require.ErrorIs(t, err, context.Canceled)
}
