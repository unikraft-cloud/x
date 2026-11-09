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
	"io"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"github.com/unikraft/go-archivefs"
	"github.com/unikraft/go-archivefs/erofs"
)

type tarEntry struct {
	hdr  tar.Header
	data string
}

func tarBlob(t *testing.T, entries ...tarEntry) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := e.hdr
		hdr.Format = tar.FormatPAX
		hdr.Size = int64(len(e.data))
		if hdr.Mode == 0 {
			hdr.Mode = 0o644
		}
		require.NoError(t, tw.WriteHeader(&hdr))
		_, err := tw.Write([]byte(e.data))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

func writeBlob(t *testing.T, store content.Store, mediaType string, data []byte, annotations map[string]string) ocispec.Descriptor {
	desc := ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(data), Size: int64(len(data)), Annotations: annotations}
	require.NoError(t, content.WriteBlob(t.Context(), store, desc.Digest.String(), bytes.NewReader(data), desc))
	return desc
}

func writeJSON(t *testing.T, store content.Store, mediaType string, v any) ocispec.Descriptor {
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return writeBlob(t, store, mediaType, data, nil)
}

// genericImage writes an image with a manifest for each platform, and returns its store and index.
func genericImage(t *testing.T, mfstAnnotations, layerAnnotations map[string]string, plats ...ocispec.Platform) (content.Store, ocispec.Descriptor) {
	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)

	base := tarBlob(t,
		tarEntry{hdr: tar.Header{Typeflag: tar.TypeDir, Name: "bin/", Mode: 0o755}},
		tarEntry{hdr: tar.Header{Typeflag: tar.TypeReg, Name: "bin/sh", Mode: 0o755, PAXRecords: map[string]string{
			"SCHILY.xattr.security.capability": "cap",
		}}, data: "elf"},
		tarEntry{hdr: tar.Header{Typeflag: tar.TypeReg, Name: "etc/old"}, data: "old"},
		tarEntry{hdr: tar.Header{Typeflag: tar.TypeReg, Name: "app/main"}, data: "main"},
	)
	var gz bytes.Buffer
	gw := gzip.NewWriter(&gz)
	_, err = gw.Write(tarBlob(t,
		tarEntry{hdr: tar.Header{Typeflag: tar.TypeReg, Name: "etc/.wh.old"}},
		tarEntry{hdr: tar.Header{Typeflag: tar.TypeReg, Name: "app/.wh.main"}},
	))
	require.NoError(t, err)
	require.NoError(t, gw.Close())
	var zs bytes.Buffer
	zw, err := zstd.NewWriter(&zs)
	require.NoError(t, err)
	_, err = zw.Write(tarBlob(t, tarEntry{hdr: tar.Header{Typeflag: tar.TypeReg, Name: "etc/new"}, data: "new"}))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	layers := []ocispec.Descriptor{
		writeBlob(t, store, ocispec.MediaTypeImageLayer, base, layerAnnotations),
		writeBlob(t, store, images.MediaTypeDockerSchema2LayerGzip, gz.Bytes(), nil),
		writeBlob(t, store, ocispec.MediaTypeImageLayerZstd, zs.Bytes(), nil),
	}
	var idx ocispec.Index
	idx.SchemaVersion, idx.MediaType = 2, ocispec.MediaTypeImageIndex
	idx.Annotations = map[string]string{ocispec.AnnotationRefName: "test"}
	for _, p := range plats {
		config := writeJSON(t, store, ocispec.MediaTypeImageConfig, ocispec.Image{
			Platform: p,
			Config: ocispec.ImageConfig{
				Entrypoint: []string{"/bin/sh", "-c"},
				Cmd:        []string{"echo hi"},
				WorkingDir: "/app",
				Env:        []string{"PATH=/bin", "PWD=/"},
			},
		})
		mfst := writeJSON(t, store, ocispec.MediaTypeImageManifest, ocispec.Manifest{
			SchemaVersion: 2,
			MediaType:     ocispec.MediaTypeImageManifest,
			Config:        config,
			Layers:        layers,
			Annotations:   mfstAnnotations,
		})
		mfst.Platform = &p
		idx.Manifests = append(idx.Manifests, mfst)
	}
	return store, writeJSON(t, store, ocispec.MediaTypeImageIndex, idx)
}

var (
	linuxAmd64   = ocispec.Platform{OS: "linux", Architecture: "amd64"}
	linuxArm64   = ocispec.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}
	linuxAmd64v3 = ocispec.Platform{OS: "linux", Architecture: "amd64", Variant: "v3"}
)

func TestLoadGeneric(t *testing.T) {
	// The temporary EROFS image goes to TMPDIR, so that the test sees it.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	store, desc := genericImage(t, nil, nil, linuxAmd64v3, linuxAmd64, linuxArm64)

	for _, tc := range []struct {
		name string
		want ocispec.Platform
		arch string
	}{
		{"kraftcloud", ocispec.Platform{OS: PlatformKraftcloud, Architecture: ArchitectureIntel}, "amd64"},
		{"fc arm64", ocispec.Platform{OS: PlatformFirecracker, Architecture: ArchitectureArm}, "arm64"},
		{"linux", linuxAmd64, "amd64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img, err := LoadContent(t.Context(), store, desc, platforms.Only(tc.want))
			require.NoError(t, err)
			require.True(t, img.Generic)
			require.Equal(t, tc.arch, img.Image.Architecture)
			if tc.want.OS == "linux" {
				// A load for linux, such as the OCI rootfs of a build, does not change the image.
				require.Nil(t, img.Initrd)
				require.Equal(t, []string{"/bin/sh", "-c"}, img.Image.Config.Entrypoint)
				require.NoError(t, img.Close())
				return
			}
			require.NotEqual(t, "v3", img.Image.Variant)
			require.Equal(t, "test", img.IndexAnnotations[ocispec.AnnotationRefName])
			require.Equal(t, []string{"/bin/sh", "-c", "echo hi"}, img.Image.Config.Cmd)
			require.Empty(t, img.Image.Config.Entrypoint)
			require.Equal(t, []string{"PATH=/bin", "PWD=/app"}, img.Image.Config.Env)
			desc, _ := img.Initrd.Source()
			require.Empty(t, desc.Digest)

			data := readInitrd(t, img.Initrd)
			again := readInitrd(t, img.Initrd)
			require.True(t, bytes.Equal(data, again), "all opens read the same image")

			fsys, err := erofs.Open(bytes.NewReader(data))
			require.NoError(t, err)
			for name, want := range map[string]string{"bin/sh": "elf", "etc/new": "new"} {
				got, err := fs.ReadFile(fsys, name)
				require.NoError(t, err)
				require.Equal(t, want, string(got))
			}
			for _, name := range []string{"etc/old", "app/main"} {
				_, err := fs.Stat(fsys, name)
				require.ErrorIs(t, err, fs.ErrNotExist, name)
			}
			fi, err := fs.Stat(fsys, "bin/sh")
			require.NoError(t, err)
			xattrs, err := archivefs.GetXattrs(fi.Sys())
			require.NoError(t, err)
			require.Equal(t, "cap", xattrs["security.capability"])

			require.NoError(t, img.Close())
			left, err := filepath.Glob(filepath.Join(tmp, "rootfs-*.erofs"))
			require.NoError(t, err)
			require.Empty(t, left, "Close must remove the EROFS image")
		})
	}

	all, err := LoadAllContent(t.Context(), store, desc, platforms.Only(ocispec.Platform{OS: PlatformQemu, Architecture: ArchitectureIntel}))
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, "amd64", all[0].Image.Architecture)

	_, err = LoadContent(t.Context(), store, desc, platforms.Only(ocispec.Platform{OS: PlatformKraftcloud, Architecture: "riscv64"}))
	require.ErrorIs(t, err, errdefs.ErrNotFound)
}

func TestLoadGenericManifest(t *testing.T) {
	t.Parallel()

	// The root descriptor is a manifest, so the loader does not check its platform.
	store, idx := genericImage(t, nil, nil, linuxAmd64)
	blob, err := content.ReadBlob(t.Context(), store, idx)
	require.NoError(t, err)
	var index ocispec.Index
	require.NoError(t, json.Unmarshal(blob, &index))
	mfst := index.Manifests[0]
	mfst.Platform = nil

	kraftcloud := platforms.Only(ocispec.Platform{OS: PlatformKraftcloud, Architecture: ArchitectureIntel})
	img, err := LoadContent(t.Context(), store, mfst, kraftcloud)
	require.NoError(t, err)
	require.True(t, img.Generic)
	require.NotNil(t, img.Initrd)
	require.NoError(t, img.Close())

	all, err := LoadAllContent(t.Context(), store, mfst, kraftcloud)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.NotNil(t, all[0].Initrd)
	require.NoError(t, all[0].Close())

	img, err = LoadContent(t.Context(), store, mfst, platforms.Only(linuxAmd64))
	require.NoError(t, err)
	require.True(t, img.Generic)
	require.Nil(t, img.Initrd)
}

func TestLoadNotGeneric(t *testing.T) {
	t.Parallel()

	kraftcloud := platforms.Only(ocispec.Platform{OS: PlatformKraftcloud, Architecture: ArchitectureIntel})
	for name, annotations := range map[string][2]map[string]string{
		"manifest": {{AnnotationKernelVersion: "6.12"}, nil},
		"layer":    {nil, {AnnotationKernelInitrdPath: "/initrd"}},
	} {
		store, desc := genericImage(t, annotations[0], annotations[1], linuxAmd64)
		_, err := LoadContent(t.Context(), store, desc, kraftcloud)
		require.ErrorIs(t, err, errdefs.ErrNotFound, name)

		img, err := LoadContent(t.Context(), store, desc, platforms.Only(linuxAmd64))
		require.NoError(t, err, name)
		require.False(t, img.Generic, name)
		require.Equal(t, []string{"/bin/sh", "-c"}, img.Image.Config.Entrypoint, name)
	}
}

func TestLoadGenericError(t *testing.T) {
	t.Parallel()

	// The config of the only generic manifest is not JSON, and Load must say so.
	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	mfst := writeJSON(t, store, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		SchemaVersion: 2,
		MediaType:     ocispec.MediaTypeImageManifest,
		Config:        writeBlob(t, store, ocispec.MediaTypeImageConfig, []byte("not json"), nil),
		Layers:        []ocispec.Descriptor{writeBlob(t, store, ocispec.MediaTypeImageLayer, tarBlob(t), nil)},
	})
	mfst.Platform = &linuxAmd64
	idx := writeJSON(t, store, ocispec.MediaTypeImageIndex, ocispec.Index{
		SchemaVersion: 2,
		MediaType:     ocispec.MediaTypeImageIndex,
		Manifests:     []ocispec.Descriptor{mfst},
	})
	_, err = LoadContent(t.Context(), store, idx, platforms.Only(ocispec.Platform{OS: PlatformKraftcloud, Architecture: ArchitectureIntel}))
	require.ErrorContains(t, err, "unmarshal")
}

func readInitrd(t *testing.T, f File) []byte {
	rc, size, err := f.Open(context.Background())
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, size, int64(len(data)))
	return data
}

func TestIsGeneric(t *testing.T) {
	t.Parallel()

	layer := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayerGzip}
	for name, tc := range map[string]struct {
		mfst ocispec.Manifest
		want bool
	}{
		"tar layers":       {ocispec.Manifest{Layers: []ocispec.Descriptor{layer, layer}}, true},
		"no layers":        {ocispec.Manifest{}, true},
		"rom":              {ocispec.Manifest{Layers: []ocispec.Descriptor{layer, {MediaType: MediaTypeRom}}}, false},
		"encrypted layer":  {ocispec.Manifest{Layers: []ocispec.Descriptor{{MediaType: ocispec.MediaTypeImageLayerGzip + "+encrypted"}}}, false},
		"other annotation": {ocispec.Manifest{Layers: []ocispec.Descriptor{layer}, Annotations: map[string]string{ocispec.AnnotationCreated: "now"}}, true},
		"kraftkit":         {ocispec.Manifest{Layers: []ocispec.Descriptor{layer}, Annotations: map[string]string{AnnotationName: "x"}}, false},
	} {
		require.Equal(t, tc.want, isGeneric(tc.mfst), name)
	}
}

func TestRunnableConfig(t *testing.T) {
	t.Parallel()

	cfg := ocispec.ImageConfig{Cmd: []string{"nginx"}}
	runnableConfig(&cfg)
	require.Equal(t, []string{"nginx"}, cfg.Cmd)
	require.Empty(t, cfg.Env)
}
