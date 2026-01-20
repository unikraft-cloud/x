// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestUnpackNewImage(t *testing.T) {
	// create a new image, and unpack it to verify on-disk contents
	img := testNewImage()
	testUnpackImage(t, img)
}

func TestUnpackSavedImage(t *testing.T) {
	// create a new new image, save it to a containerd content store,
	// then load it back and unpack to verify on-disk contents
	ctx := t.Context()

	contentDir := t.TempDir()

	img := testNewImage()
	desc, err := SaveOCILayoutNamed(ctx, contentDir, "test-image", img)
	require.NoError(t, err)
	require.Equal(t, ocispec.MediaTypeImageIndex, desc.MediaType)

	newImg, err := LoadOCILayoutNamed(ctx, contentDir, "test-image")
	require.NoError(t, err)
	testUnpackImage(t, newImg)

	newImg, err = LoadOCILayout(ctx, contentDir, desc)
	require.NoError(t, err)
	testUnpackImage(t, newImg)
}

func testNewImage() *Image {
	image := NewImage(
		WithKernel(&staticFile{"kernel.img", []byte("kernel data")}),
		WithInitrd(&staticFile{"initrd.img", []byte("initrd data")}),
		WithRom(&staticFile{"rom.img", []byte("rom data")}),
		WithImageConfig(ocispec.ImageConfig{
			Cmd: []string{"example", "--flag"},
		}),
	)
	return image
}

func testUnpackImage(t *testing.T, image *Image) {
	ctx := t.Context()
	dest := t.TempDir()

	err := Unpack(ctx, image, dest)
	require.NoError(t, err)

	var paths []string
	err = filepath.WalkDir(dest, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		path, err = filepath.Rel(dest, path)
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		if d.IsDir() {
			path += "/"
		}
		paths = append(paths, path)
		return nil
	})
	require.NoError(t, err)

	expectedPaths := []string{
		"unikraft/",
		"unikraft/bin/",
		"unikraft/bin/initrd",
		"unikraft/bin/kernel",
		"unikraft/bin/rom",
		"unikraft/cmdline.txt",
		"unikraft/config.json",
	}
	require.Equal(t, expectedPaths, paths)

	dt, err := os.ReadFile(filepath.Join(dest, "unikraft/bin/kernel"))
	require.NoError(t, err)
	require.Equal(t, []byte("kernel data"), dt)

	dt, err = os.ReadFile(filepath.Join(dest, "unikraft/bin/initrd"))
	require.NoError(t, err)
	require.Equal(t, []byte("initrd data"), dt)

	dt, err = os.ReadFile(filepath.Join(dest, "unikraft/bin/rom"))
	require.NoError(t, err)
	require.Equal(t, []byte("rom data"), dt)

	dt, err = os.ReadFile(filepath.Join(dest, "unikraft/config.json"))
	require.NoError(t, err)
	var config ocispec.Image
	require.NoError(t, json.Unmarshal(dt, &config))
	require.Equal(t, []string{"example", "--flag"}, config.Config.Cmd)

	dt, err = os.ReadFile(filepath.Join(dest, "unikraft/cmdline.txt"))
	require.NoError(t, err)
	require.Equal(t, []byte("example --flag"), dt)
}
