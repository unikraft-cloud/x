// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"bytes"
	"cmp"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/image-spec/schemes"
)

// unikraftImage returns an image of platform p with a kernel, an initrd and what opts add.
func unikraftImage(p ocispec.Platform, opts ...NewImageOpt) *Image {
	created := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	return NewImage(append([]NewImageOpt{
		WithPlatform(p),
		WithImageConfig(ocispec.ImageConfig{Cmd: []string{"/app", "--" + p.Architecture}, Env: []string{"A=1"}}),
		WithImageMetadata(ImageMetadata{Created: &created, Author: "Unikraft"}),
		WithKernel(NewStaticFile("kernel", []byte("kernel for "+p.Architecture))),
		WithInitrd(NewStaticFile("initrd", []byte("initrd for "+p.Architecture))),
	}, opts...)...)
}

var (
	fcIntel = ocispec.Platform{OS: "fc", Architecture: ArchitectureIntel}
	fcArm   = ocispec.Platform{OS: "fc", Architecture: ArchitectureArm}
)

// loadedImage is what a load gives of an image, without where it came from.
type loadedImage struct {
	Manifest         ocispec.Descriptor
	Config           *ocispec.Image
	Annotations      map[string]string
	IndexAnnotations map[string]string
	Runtime          RuntimeKind
	Kernel           ocispec.Descriptor
	KernelDebug      ocispec.Descriptor
	Initrd           ocispec.Descriptor
	Roms             []ocispec.Descriptor
	Layers           []ocispec.Descriptor
}

func loadedImages(t *testing.T, imgs []*Image) []loadedImage {
	t.Helper()

	source := func(f File) ocispec.Descriptor {
		if f == nil {
			return ocispec.Descriptor{}
		}
		if isRootfs(f) {
			return ocispec.Descriptor{MediaType: "rootfs of the layers"}
		}
		desc, _ := f.Source()
		require.NotEmpty(t, desc.Digest, f.Path())
		return desc
	}
	loaded := make([]loadedImage, 0, len(imgs))
	for _, img := range imgs {
		l := loadedImage{
			Manifest:         img.ManifestDescriptor,
			Config:           img.Image,
			Annotations:      img.Annotations,
			IndexAnnotations: img.IndexAnnotations,
			Runtime:          img.Runtime,
			Kernel:           source(img.Kernel),
			KernelDebug:      source(img.KernelDebug),
			Initrd:           source(img.Initrd),
		}
		for _, rom := range img.Roms {
			l.Roms = append(l.Roms, source(rom))
		}
		for _, layer := range img.Layers {
			l.Layers = append(l.Layers, source(layer))
		}
		loaded = append(loaded, l)
	}
	return loaded
}

func TestLoadSaveLoad(t *testing.T) {
	_, host, resolver := startTestRegistry(t)

	// docker loads a Docker image for platform, from a store with its manifests for linux.
	docker := func(platform ocispec.Platform) func(t *testing.T) []*Image {
		return func(t *testing.T) []*Image {
			store, desc := genericImage(t, t.TempDir(), nil, linuxAmd64)
			imgs, err := LoadAllContent(t.Context(), store, desc, platforms.Only(platform))
			require.NoError(t, err)
			return imgs
		}
	}
	kinds := map[string]struct {
		images   func(t *testing.T) []*Image
		platform platforms.MatchComparer
	}{
		"every part and annotation": {images: func(*testing.T) []*Image {
			img := unikraftImage(fcIntel,
				WithDebugKernel(NewStaticFile("kernel.dbg", []byte("debug kernel"))),
				WithRom(NewStaticFile("rom0", []byte("first rom"))),
				WithRom(NewStaticFile("rom1", []byte("second rom"))),
			)
			img.Annotations[AnnotationName] = "app"
			img.IndexAnnotations = map[string]string{AnnotationBuildID: "42"}
			return []*Image{img}
		}},
		"multi-platform": {images: func(*testing.T) []*Image {
			return []*Image{unikraftImage(fcIntel), unikraftImage(fcArm)}
		}},
		"docker for linux":      {images: docker(linuxAmd64), platform: platforms.Only(linuxAmd64)},
		"docker for kraftcloud": {images: docker(kraftcloud), platform: platforms.Only(kraftcloud)},
	}
	// Each backend saves the images, and loads them again for the platform.
	backends := map[string]func(t *testing.T, name string, imgs []*Image, platform platforms.MatchComparer) (digest.Digest, []*Image){
		"layout": func(t *testing.T, name string, imgs []*Image, platform platforms.MatchComparer) (digest.Digest, []*Image) {
			return saveLoad(t, resolver, &Location{Scheme: schemes.OCILayout, Path: t.TempDir() + ":" + name}, imgs, platform)
		},
		"tarball": func(t *testing.T, name string, imgs []*Image, platform platforms.MatchComparer) (digest.Digest, []*Image) {
			return saveLoad(t, resolver, &Location{Scheme: schemes.OCIArchive, Path: filepath.Join(t.TempDir(), name+".tar")}, imgs, platform)
		},
		"registry": func(t *testing.T, name string, imgs []*Image, platform platforms.MatchComparer) (digest.Digest, []*Image) {
			return saveLoad(t, resolver, &Location{Scheme: schemes.OCI, Path: host + "/acme/" + name + ":v1"}, imgs, platform)
		},
		"store": func(t *testing.T, name string, imgs []*Image, platform platforms.MatchComparer) (digest.Digest, []*Image) {
			store, err := local.NewStore(t.TempDir())
			require.NoError(t, err)
			desc, err := SaveContent(t.Context(), store, name, imgs...)
			require.NoError(t, err)
			loaded, err := LoadAllContent(t.Context(), store, desc, platform)
			require.NoError(t, err)
			return desc.Digest, loaded
		},
	}

	for kind, tc := range kinds {
		for backend, saveLoad := range backends {
			t.Run(kind+"/"+backend, func(t *testing.T) {
				platform := cmp.Or[platforms.MatchComparer](tc.platform, platforms.All)
				images := tc.images(t)
				saved, first := saveLoad(t, "first", images, platform)
				again, second := saveLoad(t, "second", first, platform)

				require.Equal(t, saved, again)
				require.Equal(t, loadedImages(t, first), loadedImages(t, second))
				if tc.platform != nil {
					// The save of a Docker image keeps all but its manifest.
					want, got := loadedImages(t, images), loadedImages(t, first)
					for i := range want {
						want[i].Manifest, got[i].Manifest = ocispec.Descriptor{}, ocispec.Descriptor{}
					}
					require.Equal(t, want, got)
				}
				for _, img := range slices.Concat(images, first, second) {
					require.NoError(t, img.Close())
				}
			})
		}
	}
}

// saveLoad saves imgs to dest with an accessor of resolver, and loads them again for the platform.
func saveLoad(t *testing.T, resolver remotes.Resolver, dest *Location, imgs []*Image, platform platforms.MatchComparer) (digest.Digest, []*Image) {
	t.Helper()

	a := NewAccessor(WithResolver(resolver))
	saved, err := a.Save(t.Context(), dest, imgs...)
	require.NoError(t, err)
	loaded, err := a.LoadAll(t.Context(), saved, platform)
	require.NoError(t, err)
	return saved.Descriptor.Digest, loaded
}

func TestSaveKeepsEdits(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	a := NewAccessor()

	saved, err := a.Save(ctx, &Location{Scheme: schemes.OCILayout, Path: dir + ":v1"}, unikraftImage(fcIntel))
	require.NoError(t, err)
	img, err := a.Load(ctx, saved, platforms.Only(fcIntel))
	require.NoError(t, err)
	kernel, _ := img.Kernel.Source()

	img.Image.Config.Env = append(img.Image.Config.Env, "EDITED=1")
	img.Annotations = map[string]string{"edited": "yes"}
	edited, err := a.Save(ctx, &Location{Scheme: schemes.OCILayout, Path: dir + ":v2"}, img)
	require.NoError(t, err)
	again, err := a.Load(ctx, edited, platforms.All)
	require.NoError(t, err)

	require.NotEqual(t, img.ManifestDescriptor.Digest, again.ManifestDescriptor.Digest)
	require.Equal(t, []string{"A=1", "EDITED=1"}, again.Image.Config.Env)
	require.Equal(t, map[string]string{"edited": "yes"}, again.Annotations)
	got, _ := again.Kernel.Source()
	require.Equal(t, kernel.Digest, got.Digest)
}

func TestLoadRefusesOtherLayers(t *testing.T) {
	ctx := t.Context()
	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)

	write := func(mediaType string, data []byte, annotations map[string]string) ocispec.Descriptor {
		desc := ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(data), Size: int64(len(data)), Annotations: annotations}
		require.NoError(t, content.WriteBlob(ctx, store, desc.Digest.String(), bytes.NewReader(data), desc))
		return desc
	}
	kernel := write(ocispec.MediaTypeImageLayer, []byte("kernel"), map[string]string{AnnotationKernelPath: WellKnownKernelPath})
	files := write(ocispec.MediaTypeImageLayer, []byte("a layer of files"), nil)
	manifest := func(p ocispec.Platform, layers ...ocispec.Descriptor) ocispec.Descriptor {
		config := writeJSON(t, store, ocispec.MediaTypeImageConfig, ocispec.Image{Platform: p})
		return writeJSON(t, store, ocispec.MediaTypeImageManifest, ocispec.Manifest{
			SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest, Config: config, Layers: layers,
		})
	}

	// A Unikraft image with a layer that is no part, as the old debug images have.
	_, err = LoadAllContent(ctx, store, manifest(ocispec.Platform{OS: "kraftcloud", Architecture: "x86_64"}, kernel, files), platforms.All)
	require.ErrorContains(t, err, files.Digest.String())

	// A linux image with a layer that is no tar layer, or a windows image, loads without its layers, but does not save.
	linux := ocispec.Platform{OS: "linux", Architecture: "amd64"}
	other := write("application/octet-stream", []byte("no tar"), nil)
	for _, m := range []ocispec.Descriptor{manifest(linux, files, other), manifest(ocispec.Platform{OS: "windows", Architecture: "amd64"}, files)} {
		loaded, err := LoadAllContent(ctx, store, m, platforms.All)
		require.NoError(t, err)
		require.Len(t, loaded, 1)
		require.Equal(t, RuntimeKindUnikraft, loaded[0].Runtime)
		require.Empty(t, loaded[0].Layers)
		require.Nil(t, loaded[0].Kernel)
		require.Nil(t, loaded[0].Initrd)
		_, err = SaveContent(ctx, store, "latest", loaded...)
		require.ErrorContains(t, err, "not Unikraft parts")
		require.ErrorContains(t, err, "Accessor.Copy")
	}

	// A linux image with Unikraft parts and other layers does not save either.
	loaded, err := LoadAllContent(ctx, store, manifest(linux, kernel, files), platforms.All)
	require.NoError(t, err)
	require.NotNil(t, loaded[0].Kernel)
	_, err = SaveContent(ctx, store, "latest", loaded...)
	require.ErrorContains(t, err, "not Unikraft parts")
}

func TestLoadSkipsAttestations(t *testing.T) {
	ctx := t.Context()
	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)

	mfst, err := SaveManifest(ctx, store, "latest", unikraftImage(fcIntel))
	require.NoError(t, err)
	data := []byte(`{"_type":"https://in-toto.io/Statement/v0.1"}`)
	statement := ocispec.Descriptor{MediaType: "application/vnd.in-toto+json", Digest: digest.FromBytes(data), Size: int64(len(data))}
	require.NoError(t, content.WriteBlob(ctx, store, statement.Digest.String(), bytes.NewReader(data), statement))
	attestation := writeJSON(t, store, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest,
		Config: writeJSON(t, store, "application/vnd.oci.empty.v1+json", struct{}{}),
		Layers: []ocispec.Descriptor{statement},
	})
	attestation.Platform = &ocispec.Platform{OS: "unknown", Architecture: "unknown"}
	attestation.Annotations = map[string]string{annotationReferenceType: "attestation-manifest", "vnd.docker.reference.digest": mfst.Digest.String()}
	// The attestation comes first, where a load of one image would take it.
	idx := writeJSON(t, store, ocispec.MediaTypeImageIndex, ocispec.Index{
		SchemaVersion: 2, MediaType: ocispec.MediaTypeImageIndex, Manifests: []ocispec.Descriptor{attestation, mfst},
	})

	loaded, err := LoadAllContent(ctx, store, idx, platforms.All)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, mfst.Digest, loaded[0].ManifestDescriptor.Digest)
	require.NotNil(t, loaded[0].Kernel)

	img, err := LoadContent(ctx, store, idx, platforms.All)
	require.NoError(t, err)
	require.Equal(t, mfst.Digest, img.ManifestDescriptor.Digest)
}
