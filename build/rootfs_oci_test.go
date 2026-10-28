// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"cmp"
	"context"
	"os"
	"path/filepath"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/kraftfile"

	"unikraft.com/x/build/internal/buildfs"
)

// writeUnikraftOCIArchive packages srcDir into a CPIO initrd, wraps it in a
// unikraft-style OCI image (with a dedicated initrd component), and saves the
// result as an OCI archive tarball. Extra image options, e.g. a config to
// inherit, are appended. It returns the path to the tarball.
func writeUnikraftOCIArchive(t *testing.T, srcDir string, extra ...imagespec.NewImageOpt) string {
	t.Helper()

	cpioPath := filepath.Join(t.TempDir(), "initrd.cpio")
	f, err := os.Create(cpioPath)
	require.NoError(t, err)
	defer f.Close()

	ctx := context.Background()
	require.NoError(t, buildfs.CreateCPIO(ctx, f, os.DirFS(srcDir)))
	require.NoError(t, f.Sync())

	cpioFile, err := os.Open(cpioPath)
	require.NoError(t, err)
	t.Cleanup(func() { cpioFile.Close() })

	img := imagespec.NewImage(append([]imagespec.NewImageOpt{
		imagespec.WithPlatform(ocispec.Platform{OS: "fc", Architecture: "x86_64"}),
		imagespec.WithInitrd(imagespec.NewOSFile(cpioFile)),
	}, extra...)...)

	archivePath := filepath.Join(t.TempDir(), "unikraft-image.tar")
	_, err = imagespec.SaveTarball(ctx, archivePath, img)
	require.NoError(t, err)
	return archivePath
}

// TestRootfsOCIUnikraftImage reads a unikraft-style OCI image that carries a
// dedicated initrd component and verifies that the initrd is passed through
// untouched, without being repackaged.
func TestRootfsOCIUnikraftImage(t *testing.T) {
	srcDir := writeTestDirectory(t)
	archivePath := writeUnikraftOCIArchive(t, srcDir)

	for _, format := range []kraftfile.FsType{"", kraftfile.FsTypeCpio} {
		t.Run(cmp.Or(string(format), "unset"), func(t *testing.T) {
			imgs := runBuildRootfs(t, BuildOpts{
				Rootfs: FSOpts{
					Path:   "oci-archive://" + archivePath,
					Type:   kraftfile.SourceTypeOCI,
					Format: format,
				},
				Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
			})
			require.Len(t, imgs, 1)

			files := readCpioInitrd(t, imgs[0])
			require.Contains(t, files, "./hello.txt")
			require.Equal(t, "hello\n", files["./hello.txt"])
			require.Contains(t, files, "./subdir/nested.txt")
			require.Equal(t, "nested\n", files["./subdir/nested.txt"])
		})
	}
}

// TestRootfsOCIUnikraftImageFormatMismatch asserts that a format the initrd
// cannot satisfy is reported rather than silently ignored.
func TestRootfsOCIUnikraftImageFormatMismatch(t *testing.T) {
	srcDir := writeTestDirectory(t)
	archivePath := writeUnikraftOCIArchive(t, srcDir)

	_, err := Builder{}.BuildRootfs(t.Context(), BuildOpts{
		Rootfs: FSOpts{
			Path:   "oci-archive://" + archivePath,
			Type:   kraftfile.SourceTypeOCI,
			Format: kraftfile.FsTypeErofs,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.ErrorContains(t, err, "rootfs format mismatch")
}

// TestRootfsOCIUnikraftImageTooLarge asserts that an initrd past
// MaxRootfsSize fails the build, and one at the limit does not.
func TestRootfsOCIUnikraftImageTooLarge(t *testing.T) {
	srcDir := writeTestDirectory(t)
	archivePath := writeUnikraftOCIArchive(t, srcDir)
	opts := BuildOpts{
		Rootfs:   FSOpts{Path: "oci-archive://" + archivePath, Type: kraftfile.SourceTypeOCI},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	}

	imgs := runBuildRootfs(t, opts)
	require.Len(t, imgs, 1)
	rc, size, err := imgs[0].Initrd.Open(t.Context())
	require.NoError(t, err)
	require.NoError(t, rc.Close())

	_, err = Builder{TempDir: t.TempDir(), MaxRootfsSize: size - 1}.BuildRootfs(t.Context(), opts)
	require.ErrorIs(t, err, ErrTooLarge)

	imgs, err = Builder{TempDir: t.TempDir(), MaxRootfsSize: size}.BuildRootfs(t.Context(), opts)
	require.NoError(t, err)
	for _, img := range imgs {
		require.NoError(t, img.Close())
	}
}

// TestRootfsOCIUnikraftImageConfig asserts that the source image's config
// survives, and that the build options still override it.
func TestRootfsOCIUnikraftImageConfig(t *testing.T) {
	srcDir := writeTestDirectory(t)
	archivePath := writeUnikraftOCIArchive(t, srcDir, imagespec.WithImageConfig(ocispec.ImageConfig{
		Cmd: []string{"/from-image"},
		Env: []string{"FROM_IMAGE=1", "SHADOWED=image"},
	}))

	t.Run("inherited", func(t *testing.T) {
		imgs := runBuildRootfs(t, BuildOpts{
			Rootfs: FSOpts{
				Path: "oci-archive://" + archivePath,
				Type: kraftfile.SourceTypeOCI,
			},
			Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
		})
		require.Len(t, imgs, 1)
		require.Equal(t, []string{"/from-image"}, imgs[0].Image.Config.Cmd)
		require.Contains(t, imgs[0].Image.Config.Env, "FROM_IMAGE=1")
	})

	t.Run("overridden", func(t *testing.T) {
		imgs := runBuildRootfs(t, BuildOpts{
			Rootfs: FSOpts{
				Path: "oci-archive://" + archivePath,
				Type: kraftfile.SourceTypeOCI,
			},
			Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
			Cmd:      []string{"/from-opts"},
			Env:      kraftfile.Map{{Key: "SHADOWED", Value: "opts"}},
		})
		require.Len(t, imgs, 1)
		require.Equal(t, []string{"/from-opts"}, imgs[0].Image.Config.Cmd)
		require.Equal(t, []string{"FROM_IMAGE=1", "SHADOWED=opts"},
			imgs[0].Image.Config.Env)
	})
}

// TestRomOCIUnikraftImagePadded covers the path buildRoms takes: a ROM must be
// page-aligned or the platform rejects it.
func TestRomOCIUnikraftImagePadded(t *testing.T) {
	srcDir := writeTestDirectory(t)
	archivePath := writeUnikraftOCIArchive(t, srcDir)

	roms, err := Builder{}.buildRoms(t.Context(), BuildOpts{
		Roms: []FSOpts{{
			Path:   "oci-archive://" + archivePath,
			Type:   kraftfile.SourceTypeOCI,
			Format: kraftfile.FsTypeCpio,
			Pad:    4096,
		}},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.NoError(t, err)
	require.Len(t, roms, 1)
	require.Len(t, roms[0], 1)
	t.Cleanup(func() { _ = roms[0][0].Cleanup() })

	_, size, err := roms[0][0].Open(t.Context())
	require.NoError(t, err)
	require.NotZero(t, size)
	require.Zero(t, size%4096, "rom must be padded to page alignment")
}

// TestRootfsOCISinglePlatformImageMultiplePlatforms verifies that a
// single-platform image is not silently reused for every requested platform.
func TestRootfsOCISinglePlatformImageMultiplePlatforms(t *testing.T) {
	srcDir := writeTestDirectory(t)
	archivePath := writeUnikraftOCIArchive(t, srcDir)

	_, err := Builder{}.BuildRootfs(t.Context(), BuildOpts{
		Rootfs: FSOpts{
			Path: "oci-archive://" + archivePath,
			Type: kraftfile.SourceTypeOCI,
		},
		Platform: []ocispec.Platform{
			{OS: "fc", Architecture: "x86_64"},
			{OS: "fc", Architecture: "arm64"},
		},
	})
	require.ErrorContains(t, err, "does not contain platform")
}

func TestRootfsOCIRegularImageNonRegistry(t *testing.T) {
	// Without an initrd component the image is a regular one, which only
	// BuildKit can flatten, and that needs a registry to pull from.
	archivePath := filepath.Join(t.TempDir(), "regular-image.tar")
	img := imagespec.NewImage(imagespec.WithPlatform(ocispec.Platform{OS: "linux", Architecture: "amd64"}))
	_, err := imagespec.SaveTarball(t.Context(), archivePath, img)
	require.NoError(t, err)

	_, err = Builder{}.BuildRootfs(t.Context(), BuildOpts{
		Rootfs: FSOpts{
			Path:   "oci-archive://" + archivePath,
			Type:   kraftfile.SourceTypeOCI,
			Format: kraftfile.FsTypeCpio,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.ErrorContains(t, err, "must be a registry reference")
}
