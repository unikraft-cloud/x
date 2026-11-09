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
	"os"
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

	"unikraft.com/x/image-spec/schemes"
	"unikraft.com/x/mergefs"
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

// genericImage writes an image with a manifest for each platform to the store in dir, and returns the store and the index.
func genericImage(t *testing.T, dir string, layerAnnotations map[string]string, plats ...ocispec.Platform) (content.Store, ocispec.Descriptor) {
	store, err := local.NewStore(dir)
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
		})
		mfst.Platform = &p
		idx.Manifests = append(idx.Manifests, mfst)
	}
	return store, writeJSON(t, store, ocispec.MediaTypeImageIndex, idx)
}

var (
	linuxAmd64    = ocispec.Platform{OS: "linux", Architecture: "amd64"}
	linuxArm64    = ocispec.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}
	linuxAmd64v3  = ocispec.Platform{OS: "linux", Architecture: "amd64", Variant: "v3"}
	kraftcloud    = ocispec.Platform{OS: PlatformKraftcloud, Architecture: ArchitectureIntel}
	kraftcloudArm = ocispec.Platform{OS: PlatformKraftcloud, Architecture: ArchitectureArm}
)

func TestLoadGeneric(t *testing.T) {
	// The temporary EROFS image goes to TMPDIR, so that the test sees it.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	store, desc := genericImage(t, t.TempDir(), nil, linuxAmd64v3, linuxAmd64, linuxArm64)

	for _, tc := range []struct {
		name string
		want ocispec.Platform
		arch string
	}{
		{"kraftcloud", kraftcloud, "amd64"},
		{"fc arm64", ocispec.Platform{OS: PlatformFirecracker, Architecture: ArchitectureArm}, "arm64"},
		{"linux", linuxAmd64, "amd64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img, err := LoadContent(t.Context(), store, desc, platforms.Only(tc.want))
			require.NoError(t, err)
			require.Equal(t, RuntimeKindDocker, img.Runtime)
			require.Equal(t, tc.arch, img.Image.Architecture)
			require.Len(t, img.Layers, 3)
			if tc.want.OS == "linux" {
				// A load for linux, such as the OCI rootfs of a build, does not change the image.
				require.Nil(t, img.Initrd)
				require.Equal(t, []string{"/bin/sh", "-c"}, img.Image.Config.Entrypoint)
				require.NoError(t, img.Close())
				return
			}
			require.NotEqual(t, "v3", img.Image.Variant)
			require.Equal(t, "test", img.IndexAnnotations[ocispec.AnnotationRefName])
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
			left, err := filepath.Glob(filepath.Join(tmp, "rootfs-*"))
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
	store, idx := genericImage(t, t.TempDir(), nil, linuxAmd64)
	blob, err := content.ReadBlob(t.Context(), store, idx)
	require.NoError(t, err)
	var index ocispec.Index
	require.NoError(t, json.Unmarshal(blob, &index))
	mfst := index.Manifests[0]
	mfst.Platform = nil

	img, err := LoadContent(t.Context(), store, mfst, platforms.Only(kraftcloud))
	require.NoError(t, err)
	require.Equal(t, RuntimeKindDocker, img.Runtime)
	require.NotNil(t, img.Initrd)
	require.NoError(t, img.Close())

	all, err := LoadAllContent(t.Context(), store, mfst, platforms.Only(kraftcloud))
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.NotNil(t, all[0].Initrd)
	require.NoError(t, all[0].Close())

	img, err = LoadContent(t.Context(), store, mfst, platforms.Only(linuxAmd64))
	require.NoError(t, err)
	require.Equal(t, RuntimeKindDocker, img.Runtime)
	require.Nil(t, img.Initrd)

	// The load normalizes the variant of the config, from an index and from a manifest.
	for _, variant := range []string{"v8.0", "8"} {
		store, idx := genericImage(t, t.TempDir(), nil, ocispec.Platform{OS: "linux", Architecture: "arm64", Variant: variant})
		var index ocispec.Index
		require.NoError(t, json.Unmarshal(readBlob(t, store, idx), &index))
		for _, desc := range []ocispec.Descriptor{idx, index.Manifests[0]} {
			img, err := LoadContent(t.Context(), store, desc, platforms.Only(kraftcloudArm))
			require.NoError(t, err, variant)
			require.Equal(t, RuntimeKindDocker, img.Runtime, variant)
			require.NotNil(t, img.Initrd, variant)
			require.NoError(t, img.Close())
		}
	}
}

func TestLoadNotGeneric(t *testing.T) {
	t.Parallel()

	// An image of a Unikraft platform is no Docker image, also with only tar layers, and its other layers fail the load.
	store, desc := genericImage(t, t.TempDir(), map[string]string{AnnotationKernelInitrdPath: "/initrd"}, kraftcloud)
	_, err := LoadContent(t.Context(), store, desc, platforms.Only(kraftcloud))
	require.ErrorContains(t, err, "is not a kernel, an initrd or a ROM")
}

func TestSaveGeneric(t *testing.T) {
	t.Parallel()

	store, desc := genericImage(t, t.TempDir(), nil, linuxAmd64)
	converts := 0
	count := WithRootfs(t.TempDir(), func(ctx context.Context, dst *os.File, layers []mergefs.Layer, opts ...mergefs.Option) error {
		converts++
		return mergefs.EROFS(ctx, dst, layers, opts...)
	})
	img, err := LoadContent(t.Context(), store, desc, platforms.Only(kraftcloud), count)
	require.NoError(t, err)

	// The save writes the layers back as they are, with the linux config, and does not convert them.
	saved, err := SaveManifest(t.Context(), store, "saved", img)
	require.NoError(t, err)
	require.Zero(t, converts)
	require.Equal(t, &linuxAmd64, saved.Platform)
	var mfst, orig ocispec.Manifest
	require.NoError(t, json.Unmarshal(readBlob(t, store, saved), &mfst))
	require.NoError(t, json.Unmarshal(readBlob(t, store, img.ManifestDescriptor), &orig))
	require.Equal(t, orig.Layers, mfst.Layers)

	again, err := LoadContent(t.Context(), store, saved, platforms.Only(kraftcloud), count)
	require.NoError(t, err)
	require.Equal(t, RuntimeKindDocker, again.Runtime)
	readInitrd(t, again.Initrd)
	require.Equal(t, 1, converts)
	require.NoError(t, again.Close())

	// A Docker image does not take another Initrd, but a Unikraft image made of it does.
	img.Initrd = NewStaticFile("", []byte("initrd"))
	_, err = SaveManifest(t.Context(), store, "replaced", img)
	require.ErrorContains(t, err, "RuntimeKindUnikraft")
	img.Runtime, img.Layers, img.Image.Platform = RuntimeKindUnikraft, nil, kraftcloud
	replacedDesc, err := SaveContent(t.Context(), store, "replaced", img)
	require.NoError(t, err)
	require.NoError(t, img.Close())
	replaced, err := LoadContent(t.Context(), store, replacedDesc, platforms.Only(kraftcloud), count)
	require.NoError(t, err)
	require.Equal(t, RuntimeKindUnikraft, replaced.Runtime)
	require.Empty(t, replaced.Layers)
	r, _, err := replaced.Initrd.Open(t.Context())
	require.NoError(t, err)
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	require.Equal(t, "initrd", string(data))
	require.NoError(t, replaced.Close())
	require.Equal(t, 1, converts)

	// Layers belong to a Docker image only.
	unikraft := unikraftImage(fcIntel)
	unikraft.Layers = again.Layers
	_, err = SaveManifest(t.Context(), store, "layers", unikraft)
	require.ErrorContains(t, err, "only a Docker image has layers")
}

func TestLoadGenericCPIO(t *testing.T) {
	t.Parallel()

	layout, tmp := t.TempDir(), t.TempDir()
	_, desc := genericImage(t, layout, nil, linuxAmd64)
	cpio := WithRootfs(tmp, func(ctx context.Context, dst *os.File, layers []mergefs.Layer, opts ...mergefs.Option) error {
		return mergefs.CPIO(ctx, dst, layers, opts...)
	})
	res := Resolved{Location: Location{Scheme: schemes.OCILayout, Path: layout}, Descriptor: desc}
	img, err := NewAccessor().Load(t.Context(), res, platforms.Only(kraftcloud), cpio)
	require.NoError(t, err)

	data := readInitrd(t, img.Initrd)
	require.Equal(t, "070701", string(data[:6]))
	left, err := os.ReadDir(tmp)
	require.NoError(t, err)
	require.Len(t, left, 1, "the rootfs is in the directory of the option")
	require.NoError(t, img.Close())
	left, err = os.ReadDir(tmp)
	require.NoError(t, err)
	require.Empty(t, left)
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
	_, err = LoadContent(t.Context(), store, idx, platforms.Only(kraftcloud))
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
	image := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig}
	part := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayer, Annotations: map[string]string{AnnotationKernelInitrdPath: WellKnownInitrdPath}}
	for name, tc := range map[string]struct {
		mfst ocispec.Manifest
		plat ocispec.Platform
		want bool
	}{
		"tar layers":        {ocispec.Manifest{Config: image, Layers: []ocispec.Descriptor{layer, layer}}, linuxAmd64, true},
		"docker config":     {ocispec.Manifest{Config: ocispec.Descriptor{MediaType: images.MediaTypeDockerSchema2Config}, Layers: []ocispec.Descriptor{layer}}, linuxArm64, true},
		"no layers":         {ocispec.Manifest{Config: image}, linuxAmd64, false},
		"artifact config":   {ocispec.Manifest{Config: ocispec.Descriptor{MediaType: ocispec.MediaTypeEmptyJSON}, Layers: []ocispec.Descriptor{layer}}, linuxAmd64, false},
		"rom":               {ocispec.Manifest{Config: image, Layers: []ocispec.Descriptor{layer, {MediaType: MediaTypeRom}}}, linuxAmd64, false},
		"encrypted layer":   {ocispec.Manifest{Config: image, Layers: []ocispec.Descriptor{{MediaType: ocispec.MediaTypeImageLayerGzip + "+encrypted"}}}, linuxAmd64, false},
		"unikraft platform": {ocispec.Manifest{Config: image, Layers: []ocispec.Descriptor{layer}}, kraftcloud, false},
		"unikraft part":     {ocispec.Manifest{Config: image, Layers: []ocispec.Descriptor{layer, part}}, linuxAmd64, false},
	} {
		require.Equal(t, tc.want, IsGeneric(tc.mfst, tc.plat), name)
	}
}

func TestGenericPlatforms(t *testing.T) {
	intel := GenericPlatforms(platforms.Only(kraftcloud))
	arm := GenericPlatforms(platforms.Only(kraftcloudArm))
	for _, tc := range []struct {
		m    platforms.MatchComparer
		p    ocispec.Platform
		want bool
	}{
		{intel, ocispec.Platform{OS: "linux", Architecture: "amd64"}, true},
		{intel, ocispec.Platform{OS: "linux", Architecture: "amd64", Variant: "v1"}, true},
		{intel, ocispec.Platform{OS: "linux", Architecture: "x86_64"}, true},
		{intel, ocispec.Platform{OS: "linux", Architecture: "amd64", Variant: "v3"}, false},
		{intel, ocispec.Platform{OS: "linux", Architecture: "arm64"}, false},
		{intel, ocispec.Platform{OS: "windows", Architecture: "amd64"}, false},
		{intel, ocispec.Platform{Architecture: "amd64"}, false},
		{arm, ocispec.Platform{OS: "linux", Architecture: "arm64"}, true},
		{arm, ocispec.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}, true},
		{arm, ocispec.Platform{OS: "linux", Architecture: "arm64", Variant: "v8.0"}, true},
		{arm, ocispec.Platform{OS: "linux", Architecture: "arm64", Variant: "8"}, true},
		{arm, ocispec.Platform{OS: "linux", Architecture: "aarch64"}, true},
		{arm, ocispec.Platform{OS: "linux", Architecture: "arm64", Variant: "v9"}, false},
		{arm, ocispec.Platform{OS: "linux", Architecture: "amd64"}, false},
	} {
		require.Equal(t, tc.want, tc.m.Match(tc.p), platforms.Format(tc.p))
	}
}

func TestLoadMissingBlob(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		plats     []ocispec.Platform
		noPlat    bool // the index entries have no platform, so the load reads each config
		generic   bool // the load must report the error of the generic image
		wantError string
	}{
		{name: "matched manifest", plats: []ocispec.Platform{kraftcloud, linuxAmd64}},
		{name: "matched manifest without index platforms", plats: []ocispec.Platform{kraftcloud, linuxAmd64}, noPlat: true},
		{name: "generic manifest", plats: []ocispec.Platform{linuxAmd64}, generic: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The config of the first manifest is missing, and the load must report it, not load another image.
			store, idx := genericImage(t, t.TempDir(), nil, tc.plats...)
			var index ocispec.Index
			require.NoError(t, json.Unmarshal(readBlob(t, store, idx), &index))
			var mfst ocispec.Manifest
			require.NoError(t, json.Unmarshal(readBlob(t, store, index.Manifests[0]), &mfst))
			require.NoError(t, store.Delete(t.Context(), mfst.Config.Digest))
			if tc.noPlat {
				for i := range index.Manifests {
					index.Manifests[i].Platform = nil
				}
				idx = writeJSON(t, store, ocispec.MediaTypeImageIndex, index)
			}
			_, err := LoadAllContent(t.Context(), store, idx, platforms.Only(kraftcloud))
			require.ErrorIs(t, err, errdefs.ErrNotFound)
			require.NotContains(t, err.Error(), "no match for platform")
			_, err = LoadContent(t.Context(), store, idx, platforms.Only(kraftcloud))
			require.ErrorIs(t, err, errdefs.ErrNotFound)
			require.NotContains(t, err.Error(), "no match for platform")
			if tc.generic {
				require.ErrorContains(t, err, "Docker image")
			}
		})
	}
}

func readBlob(t *testing.T, store content.Provider, desc ocispec.Descriptor) []byte {
	data, err := content.ReadBlob(t.Context(), store, desc)
	require.NoError(t, err)
	return data
}

func TestLoadGenericOSFeatures(t *testing.T) {
	t.Parallel()

	// OS features in the config do not make a load that also asks for linux convert the image, as a build does.
	withFeatures := linuxAmd64
	withFeatures.OSFeatures = []string{"x"}
	store, desc := genericImage(t, t.TempDir(), nil, withFeatures)
	img, err := LoadContent(t.Context(), store, desc, platforms.Any(kraftcloud, linuxAmd64))
	require.NoError(t, err)
	require.Equal(t, RuntimeKindDocker, img.Runtime)
	require.Nil(t, img.Initrd)
	require.NoError(t, img.Close())

	img, err = LoadContent(t.Context(), store, desc, platforms.Only(kraftcloud))
	require.NoError(t, err)
	require.NotNil(t, img.Initrd)
	require.NoError(t, img.Close())
}

func TestSaveGenericRoundTrip(t *testing.T) {
	t.Parallel()

	kc := platforms.Only(kraftcloud)
	// Each save writes the original manifest of the image and its blobs, so a load for kraftcloud converts it again.
	for name, roundTrip := range map[string]func(t *testing.T, img *Image) *Image{
		"layout": func(t *testing.T, img *Image) *Image {
			dir := t.TempDir()
			saved, err := SaveOCILayout(t.Context(), dir, "x", img)
			require.NoError(t, err)
			again, err := LoadOCILayout(t.Context(), dir, saved, kc, WithRootfs(t.TempDir(), nil))
			require.NoError(t, err)
			return again
		},
		"tarball": func(t *testing.T, img *Image) *Image {
			path := filepath.Join(t.TempDir(), "image.tar")
			saved, err := SaveTarball(t.Context(), path, img)
			require.NoError(t, err)
			again, err := LoadTarball(t.Context(), path, saved, kc, WithRootfs(t.TempDir(), nil))
			require.NoError(t, err)
			return again
		},
		"store": func(t *testing.T, img *Image) *Image {
			dst, err := local.NewStore(t.TempDir())
			require.NoError(t, err)
			saved, err := SaveContent(t.Context(), dst, "x", img)
			require.NoError(t, err)
			again, err := LoadContent(t.Context(), dst, saved, kc, WithRootfs(t.TempDir(), nil))
			require.NoError(t, err)
			return again
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, desc := genericImage(t, t.TempDir(), nil, linuxAmd64)
			img, err := LoadContent(t.Context(), store, desc, kc, WithRootfs(t.TempDir(), nil))
			require.NoError(t, err)
			defer img.Close()
			require.Equal(t, RuntimeKindDocker, img.Runtime)

			again := roundTrip(t, img)
			defer again.Close()
			require.Equal(t, RuntimeKindDocker, again.Runtime)
			require.Equal(t, readInitrd(t, img.Initrd), readInitrd(t, again.Initrd))
		})
	}
}
