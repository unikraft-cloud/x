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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	imagesarchive "github.com/containerd/containerd/v2/core/images/archive"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/platforms"
	"github.com/klauspost/compress/gzip"
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

	resaved, err := SaveContent(ctx, store, "latest", loaded...)
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

func TestDecompressLayerWindow(t *testing.T) {
	// A zstd frame of one raw byte, whose header asks for a window of 2^(10+exp) bytes.
	frame := func(exp byte) []byte {
		return []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, exp << 3, 0x09, 0x00, 0x00, 'a'}
	}
	for _, tc := range []struct {
		exp byte
		ok  bool
	}{{13, true}, {14, false}} {
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
	newImage := func(compression Compression) *Image {
		img := NewImage(
			WithKernel(&staticFile{"kernel.img", kernel}),
			WithInitrd(&staticFile{"initrd.img", initrd}),
			WithRom(&staticFile{"rom.img", []byte("rom data")}),
			WithImageConfig(ocispec.ImageConfig{Cmd: []string{"example"}}),
		)
		img.Compression = compression
		return img
	}
	save := func(dir string, compression Compression) ocispec.Descriptor {
		saved, err := NewAccessor().Save(ctx, &Location{Scheme: schemes.OCILayout, Path: dir + ":latest"}, newImage(compression))
		require.NoError(t, err)
		return saved.Descriptor
	}

	_, err := NewAccessor().Save(ctx, &Location{Scheme: schemes.OCILayout, Path: t.TempDir() + ":latest"}, newImage("gzip"))
	require.ErrorContains(t, err, `unknown compression "gzip"`)

	plainDir := t.TempDir()
	plainDesc := save(plainDir, CompressionNone)
	plain, err := local.NewStore(plainDir)
	require.NoError(t, err)
	plainMfsts, err := Manifests(ctx, plain, plainDesc, nil)
	require.NoError(t, err)
	for _, layer := range plainMfsts[0].Layers[:2] {
		require.Equal(t, ocispec.MediaTypeImageLayer, layer.MediaType, "a save without compression writes plain tar layers")
	}
	loadedPlain, err := LoadContent(ctx, plain, plainDesc, platforms.All)
	require.NoError(t, err)
	require.Equal(t, CompressionNone, loadedPlain.Compression)

	srcDir := t.TempDir()
	idxDesc := save(srcDir, CompressionZstd)
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

	require.Equal(t, idxDesc.Digest, save(t.TempDir(), CompressionZstd).Digest, "the same image compresses to the same blobs")

	loaded, err := LoadContent(ctx, src, idxDesc, platforms.All)
	require.NoError(t, err)
	require.Equal(t, CompressionZstd, loaded.Compression)
	for want, file := range map[string]File{string(kernel): loaded.Kernel, string(initrd): loaded.Initrd} {
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
	saved, err := SaveContent(ctx, dst, "latest", loaded)
	require.NoError(t, err)
	require.Equal(t, idxDesc.Digest, saved.Digest)

	// A part moves to another image with the correct DiffID.
	for name, part := range map[string]File{
		"loaded": loaded.Initrd,
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

func TestLoadCompression(t *testing.T) {
	ctx := t.Context()
	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)

	plain := NewImage(WithKernel(NewStaticFile("kernel", []byte("kernel"))))
	plainMfst, err := SaveManifest(ctx, store, "plain", plain)
	require.NoError(t, err)
	plainLoaded, err := LoadContent(ctx, store, plainMfst, platforms.All)
	require.NoError(t, err)
	zstdImg := NewImage(WithInitrd(NewStaticFile("initrd", []byte("initrd"))))
	zstdImg.Compression = CompressionZstd
	zstdMfst, err := SaveManifest(ctx, store, "zstd", zstdImg)
	require.NoError(t, err)
	zstdLoaded, err := LoadContent(ctx, store, zstdMfst, platforms.All)
	require.NoError(t, err)

	// A plain kernel and a zstd initrd: one zstd part is sufficient.
	mixed := NewImage(WithKernel(plainLoaded.Kernel), WithInitrd(zstdLoaded.Initrd))
	mixedMfst, err := SaveManifest(ctx, store, "mixed", mixed)
	require.NoError(t, err)
	loaded, err := LoadContent(ctx, store, mixedMfst, platforms.All)
	require.NoError(t, err)
	require.Equal(t, CompressionZstd, loaded.Compression)

	// Save does not write gzip, so Load does not set it.
	gzipped := gzipTarLayer(t, store, tarEntry{"kernel", []byte("kernel")})
	gzipped.Annotations = map[string]string{AnnotationKernelPath: "/kernel"}
	gzipImg := NewImage(WithKernel(NewContentStoreFile(store, gzipped, "/kernel")))
	gzipMfst, err := SaveManifest(ctx, store, "gzip", gzipImg)
	require.NoError(t, err)
	loaded, err = LoadContent(ctx, store, gzipMfst, platforms.All)
	require.NoError(t, err)
	require.Equal(t, CompressionNone, loaded.Compression)
}

// countingProvider counts the readers of each blob that it gives.
type countingProvider struct {
	content.Provider
	mu    sync.Mutex
	reads map[digest.Digest]int
}

func (p *countingProvider) ReaderAt(ctx context.Context, desc ocispec.Descriptor) (content.ReaderAt, error) {
	p.mu.Lock()
	p.reads[desc.Digest]++
	p.mu.Unlock()
	return p.Provider.ReaderAt(ctx, desc)
}

func (p *countingProvider) count(dgst digest.Digest) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reads[dgst]
}

func TestSaveComputesDiffIDs(t *testing.T) {
	ctx := t.Context()
	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)

	kernel := tarEntry{strings.TrimPrefix(WellKnownKernelPath, "/"), []byte("kernel")}
	initrd := tarEntry{strings.TrimPrefix(WellKnownInitrdPath, "/"), []byte("initrd")}
	kernelLayer := gzipTarLayer(t, store, kernel)
	kernelLayer.Annotations = map[string]string{AnnotationKernelPath: WellKnownKernelPath}
	initrdLayer := zstdTarLayer(t, store, initrd)
	initrdLayer.Annotations = map[string]string{AnnotationKernelInitrdPath: WellKnownInitrdPath}
	// The config gives the digests of the compressed layers as their DiffIDs, which is wrong.
	config := ocispec.Image{Platform: fcIntel, RootFS: ocispec.RootFS{Type: "layers", DiffIDs: []digest.Digest{kernelLayer.Digest, initrdLayer.Digest}}}
	mfst := writeJSON(t, store, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest,
		Config: writeJSON(t, store, ocispec.MediaTypeImageConfig, config),
		Layers: []ocispec.Descriptor{kernelLayer, initrdLayer},
	})
	src := &countingProvider{Provider: store, reads: map[digest.Digest]int{}}
	loaded, err := LoadAllContent(ctx, src, mfst, platforms.All)
	require.NoError(t, err)

	dst, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	saved, err := SaveContent(ctx, dst, "latest", loaded...)
	require.NoError(t, err)
	mfsts, err := Manifests(ctx, dst, saved, nil)
	require.NoError(t, err)
	data, err := content.ReadBlob(ctx, dst, mfsts[0].Config)
	require.NoError(t, err)
	var savedConfig ocispec.Image
	require.NoError(t, json.Unmarshal(data, &savedConfig))
	require.Equal(t, []digest.Digest{digest.FromBytes(tarLayer(t, kernel)), digest.FromBytes(tarLayer(t, initrd))}, savedConfig.RootFS.DiffIDs)
	require.Equal(t, 1, src.count(kernelLayer.Digest))
	require.Equal(t, 1, src.count(initrdLayer.Digest))

	// A second save to the store that now has the layers takes no wrong DiffID from the source.
	twice, err := SaveContent(ctx, dst, "twice", loaded...)
	require.NoError(t, err)
	require.Equal(t, saved.Digest, twice.Digest)
	require.Equal(t, 2, src.count(kernelLayer.Digest))
	require.Equal(t, 2, src.count(initrdLayer.Digest))

	// A save to a store that has the layers takes the DiffIDs from the config of the source, and reads no layer.
	counted := &countingProvider{Provider: dst, reads: map[digest.Digest]int{}}
	first, err := LoadAllContent(ctx, counted, saved, platforms.All)
	require.NoError(t, err)
	again, err := SaveContent(ctx, dst, "again", first...)
	require.NoError(t, err)
	require.Equal(t, saved.Digest, again.Digest)
	require.Zero(t, counted.count(kernelLayer.Digest))
	require.Zero(t, counted.count(initrdLayer.Digest))

	// The config gives one DiffID for two layers, so the save reads each layer once for its DiffID.
	config.RootFS.DiffIDs = config.RootFS.DiffIDs[:1]
	mfst = writeJSON(t, store, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest,
		Config: writeJSON(t, store, ocispec.MediaTypeImageConfig, config),
		Layers: []ocispec.Descriptor{kernelLayer, initrdLayer},
	})
	unhinted, err := LoadAllContent(ctx, src, mfst, platforms.All)
	require.NoError(t, err)
	again, err = SaveContent(ctx, dst, "unhinted", unhinted...)
	require.NoError(t, err)
	require.Equal(t, saved.Digest, again.Digest)
	require.Equal(t, 3, src.count(kernelLayer.Digest))
	require.Equal(t, 3, src.count(initrdLayer.Digest))

	other, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	resaved, err := SaveContent(ctx, other, "latest", first...)
	require.NoError(t, err)
	second, err := LoadAllContent(ctx, other, resaved, platforms.All)
	require.NoError(t, err)
	require.Equal(t, saved.Digest, resaved.Digest)
	require.Equal(t, loadedImages(t, first), loadedImages(t, second))

	// A store that resets each write gets the same image.
	reset, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	resetSaved, err := SaveContent(ctx, &resetting{Ingester: reset}, "latest", loaded...)
	require.NoError(t, err)
	require.Equal(t, saved.Digest, resetSaved.Digest)
}

func TestLoadDiffIDHint(t *testing.T) {
	ctx := t.Context()
	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)

	kernel := tarEntry{strings.TrimPrefix(WellKnownKernelPath, "/"), []byte("kernel")}
	layer := gzipTarLayer(t, store, kernel)
	layer.Annotations = map[string]string{AnnotationKernelPath: WellKnownKernelPath}
	load := func(diffID digest.Digest) *Image {
		config := ocispec.Image{Platform: fcIntel, RootFS: ocispec.RootFS{Type: "layers", DiffIDs: []digest.Digest{diffID}}}
		mfst := writeJSON(t, store, ocispec.MediaTypeImageManifest, ocispec.Manifest{
			SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest,
			Config: writeJSON(t, store, ocispec.MediaTypeImageConfig, config),
			Layers: []ocispec.Descriptor{layer},
		})
		img, err := LoadContent(ctx, store, mfst, platforms.All)
		require.NoError(t, err)
		return img
	}

	sha256 := digest.Canonical.FromBytes(tarLayer(t, kernel))
	img := load(sha256)
	require.Equal(t, sha256, diffIDHint(img.Kernel))
	clone, err := img.Kernel.Clone()
	require.NoError(t, err)
	require.Equal(t, sha256, diffIDHint(clone))

	// The hint applies only to the layer that it came with.
	img.Kernel.(*ContentStoreFile).Desc = zstdTarLayer(t, store, kernel)
	require.Empty(t, diffIDHint(img.Kernel))

	// A save computes sha256 DiffIDs only, so Load takes no other hint.
	require.Empty(t, diffIDHint(load(digest.SHA512.FromBytes(tarLayer(t, kernel))).Kernel))
}

func TestSaveDecompressionErrors(t *testing.T) {
	ctx := t.Context()
	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)

	corrupt := writeLayer(t, store, ocispec.MediaTypeImageLayerGzip, []byte("not gzip data"))
	dst, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	_, err = SaveContent(ctx, dst, "latest", NewImage(WithKernel(NewContentStoreFile(store, corrupt, WellKnownKernelPath))))
	require.ErrorContains(t, err, "failed to decompress layer "+corrupt.Digest.String())
	require.NotContains(t, err.Error(), "failed to copy content")

	large := zstdTarLayer(t, store, tarEntry{"kernel", make([]byte, 2<<20)})
	blob, err := content.ReadBlob(ctx, store, large)
	require.NoError(t, err)
	dw := newDiffIDWriter(ctx, large, 1<<20)
	_, err = io.Copy(dw, bytes.NewReader(blob))
	_, err = dw.finish(err)
	require.ErrorIs(t, err, ErrLayerTooLarge)
	require.ErrorContains(t, err, "failed to decompress layer "+large.Digest.String())
}

func TestSaveSharedPartLayer(t *testing.T) {
	ctx := t.Context()
	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)

	// One layer holds the kernel and the initrd, so the two parts have the same descriptor.
	layer := zstdTarLayer(t, store,
		tarEntry{strings.TrimPrefix(WellKnownKernelPath, "/"), []byte("kernel")},
		tarEntry{strings.TrimPrefix(WellKnownInitrdPath, "/"), []byte("initrd")},
	)
	layer.Annotations = map[string]string{AnnotationKernelPath: WellKnownKernelPath, AnnotationKernelInitrdPath: WellKnownInitrdPath}
	mfst := writeJSON(t, store, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest,
		Config: writeJSON(t, store, ocispec.MediaTypeImageConfig, ocispec.Image{Platform: fcIntel}),
		Layers: []ocispec.Descriptor{layer},
	})
	loaded, err := LoadAllContent(ctx, store, mfst, platforms.All)
	require.NoError(t, err)

	for range 10 {
		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		saved, err := SaveContent(ctx, dst, "latest", loaded...)
		require.NoError(t, err)
		again, err := LoadAllContent(ctx, dst, saved, platforms.All)
		require.NoError(t, err)
		require.Equal(t, []string{"kernel", "", "initrd"}, parts(t, again[0]))
	}
	require.Equal(t, layer.Annotations, loaded[0].Kernel.(*ContentStoreFile).Desc.Annotations, "a save does not change the source")
}

type tarEntry struct {
	name string
	data []byte
}

// tarLayer returns a tar of the entries.
func tarLayer(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.data))}))
		_, err := tw.Write(e.data)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

// writeLayer writes blob to store as a layer of mediaType.
func writeLayer(t *testing.T, store content.Ingester, mediaType string, blob []byte) ocispec.Descriptor {
	t.Helper()
	desc := ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(blob), Size: int64(len(blob))}
	require.NoError(t, content.WriteBlob(t.Context(), store, desc.Digest.String(), bytes.NewReader(blob), desc))
	return desc
}

// zstdTarLayer writes a zstd tar layer of the entries to store.
func zstdTarLayer(t *testing.T, store content.Ingester, entries ...tarEntry) ocispec.Descriptor {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	require.NoError(t, err)
	_, err = zw.Write(tarLayer(t, entries...))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return writeLayer(t, store, ocispec.MediaTypeImageLayerZstd, buf.Bytes())
}

// gzipTarLayer writes a gzip tar layer of the entries to store.
func gzipTarLayer(t *testing.T, store content.Ingester, entries ...tarEntry) ocispec.Descriptor {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write(tarLayer(t, entries...))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return writeLayer(t, store, ocispec.MediaTypeImageLayerGzip, buf.Bytes())
}

func TestLayerDiffIDLimit(t *testing.T) {
	t.Parallel()

	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	desc := zstdTarLayer(t, store, tarEntry{"initrd", make([]byte, 2<<20)})

	_, err = layerDiffID(t.Context(), store, desc, 1<<20)
	require.ErrorIs(t, err, ErrLayerTooLarge)
	id, err := layerDiffID(t.Context(), store, desc, 4<<20)
	require.NoError(t, err)
	require.Equal(t, digest.FromBytes(tarLayer(t, tarEntry{"initrd", make([]byte, 2<<20)})), id)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = layerDiffID(ctx, store, desc, 4<<20)
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

// sparseTarLayer returns a tar of a PAX GNU sparse 1.0 entry: data at offset 0, then a hole up to size.
func sparseTarLayer(t *testing.T, name string, data []byte, size int64) []byte {
	t.Helper()
	sparseMap := make([]byte, 512)
	copy(sparseMap, fmt.Sprintf("1\n0\n%d\n", len(data)))
	// The tar writer drops GNU.sparse records, so write them with a prefix of the same length and rename them.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "GNUSparseFile.0/" + name,
		Mode:     0o644,
		Size:     int64(len(sparseMap) + len(data)),
		Typeflag: tar.TypeReg,
		Format:   tar.FormatPAX,
		PAXRecords: map[string]string{
			"XNU.sparse.major":    "1",
			"XNU.sparse.minor":    "0",
			"XNU.sparse.name":     name,
			"XNU.sparse.realsize": strconv.FormatInt(size, 10),
		},
	}))
	_, err := tw.Write(append(sparseMap, data...))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	return bytes.ReplaceAll(buf.Bytes(), []byte("XNU.sparse."), []byte("GNU.sparse."))
}

func TestContentStoreFileSparse(t *testing.T) {
	t.Parallel()

	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	open := func(ctx context.Context, size, limit int64) (io.ReadCloser, int64, error) {
		desc := writeLayer(t, store, ocispec.MediaTypeImageLayer, sparseTarLayer(t, "kernel", []byte("kernel"), size))
		return (&ContentStoreFile{Store: store, Desc: desc, Path_: "/kernel", maxSize: limit}).Open(ctx)
	}

	// The blob is small, but the entry is larger than the limit.
	_, _, err = open(t.Context(), 1<<40, 0)
	require.ErrorIs(t, err, ErrLayerTooLarge)
	_, _, err = open(t.Context(), 2<<20, 1<<20)
	require.ErrorIs(t, err, ErrLayerTooLarge)

	r, size, err := open(t.Context(), 2<<20, 4<<20)
	require.NoError(t, err)
	require.Equal(t, int64(2<<20), size)
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	require.Equal(t, append([]byte("kernel"), make([]byte, 2<<20-6)...), data)

	// A read of the hole stops when the context ends.
	ctx, cancel := context.WithCancel(t.Context())
	r, _, err = open(ctx, 2<<20, 4<<20)
	require.NoError(t, err)
	_, err = io.ReadFull(r, make([]byte, 1<<10))
	require.NoError(t, err)
	cancel()
	_, err = io.ReadAll(r)
	require.ErrorIs(t, err, context.Canceled)
	r.Close()
}
