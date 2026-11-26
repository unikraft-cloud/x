// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/containerd/platforms"
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
	Kernel           ocispec.Descriptor
	KernelDebug      ocispec.Descriptor
	Initrd           ocispec.Descriptor
	Roms             []ocispec.Descriptor
}

func loadedImages(t *testing.T, imgs []*Image) []loadedImage {
	t.Helper()

	source := func(f File) ocispec.Descriptor {
		if f == nil {
			return ocispec.Descriptor{}
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
			Kernel:           source(img.Kernel),
			KernelDebug:      source(img.KernelDebug),
			Initrd:           source(img.Initrd),
		}
		for _, rom := range img.Roms {
			l.Roms = append(l.Roms, source(rom))
		}
		loaded = append(loaded, l)
	}
	return loaded
}

func TestLoadSaveLoad(t *testing.T) {
	_, host, resolver := startTestRegistry(t)

	kinds := map[string]func() []*Image{
		"every part and annotation": func() []*Image {
			img := unikraftImage(fcIntel,
				WithDebugKernel(NewStaticFile("kernel.dbg", []byte("debug kernel"))),
				WithRom(NewStaticFile("rom0", []byte("first rom"))),
				WithRom(NewStaticFile("rom1", []byte("second rom"))),
			)
			img.Annotations[AnnotationName] = "app"
			img.IndexAnnotations = map[string]string{AnnotationBuildID: "42"}
			return []*Image{img}
		},
		"multi-platform": func() []*Image {
			return []*Image{unikraftImage(fcIntel), unikraftImage(fcArm)}
		},
	}
	backends := map[string]func(t *testing.T, name string) *Location{
		"layout": func(t *testing.T, name string) *Location {
			return &Location{Scheme: schemes.OCILayout, Path: t.TempDir() + ":" + name}
		},
		"tarball": func(t *testing.T, name string) *Location {
			return &Location{Scheme: schemes.OCIArchive, Path: filepath.Join(t.TempDir(), name+".tar")}
		},
		"registry": func(t *testing.T, name string) *Location {
			return &Location{Scheme: schemes.OCI, Path: host + "/acme/" + name + ":v1"}
		},
	}

	for kind, images := range kinds {
		for backend, location := range backends {
			t.Run(kind+"/"+backend, func(t *testing.T) {
				ctx := t.Context()
				a := NewAccessor(WithResolver(resolver))

				saved, err := a.Save(ctx, location(t, "first"), images()...)
				require.NoError(t, err)
				first, err := a.LoadAll(ctx, saved, platforms.All)
				require.NoError(t, err)

				again, err := a.Save(ctx, location(t, "second"), first...)
				require.NoError(t, err)
				second, err := a.LoadAll(ctx, again, platforms.All)
				require.NoError(t, err)

				require.Equal(t, saved.Descriptor.Digest, again.Descriptor.Digest)
				require.Equal(t, loadedImages(t, first), loadedImages(t, second))
				for _, img := range append(first, second...) {
					require.NoError(t, img.Close())
				}
			})
		}
	}
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
