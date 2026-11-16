// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unikraft/go-archivefs/cpio"
	"unikraft.com/x/mergefs"
)

func TestWriteTarUnsupportedFormat(t *testing.T) {
	// A rootfs of no known format is not opened as EROFS.
	dir := t.TempDir()
	image := filepath.Join(dir, "rootfs")
	require.NoError(t, os.WriteFile(image, []byte("not a rootfs"), 0o644))
	dst := filepath.Join(dir, "rootfs.tar")
	for _, format := range []string{"", "squashfs"} {
		require.ErrorContains(t, writeTar(t.Context(), image, format, dst), "unsupported rootfs format")
		require.NoFileExists(t, dst)
	}
}

func TestToCPIOKeepsRoot(t *testing.T) {
	// The root of the EROFS image has mode 0700 and owner 1000:1001.
	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	require.NoError(t, tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "./", Mode: 0o700, Uid: 1000, Gid: 1001}))
	require.NoError(t, tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "f", Mode: 0o644}))
	require.NoError(t, tw.Close())
	image := filepath.Join(t.TempDir(), "rootfs")
	f, err := os.Create(image)
	require.NoError(t, err)
	open := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(layer.Bytes())), nil }
	err = mergefs.EROFS(t.Context(), f, []mergefs.Layer{open})
	require.NoError(t, errors.Join(err, f.Close()))

	_, _, err = toCPIO(t.Context(), image)
	require.NoError(t, err)
	data, err := os.ReadFile(image)
	require.NoError(t, err)
	fsys, err := cpio.OpenFS(bytes.NewReader(data))
	require.NoError(t, err)
	fi, err := fsys.Stat(".")
	require.NoError(t, err)
	require.Equal(t, fs.ModeDir|0o700, fi.Mode())
	hdr, ok := fi.Sys().(*cpio.Header)
	require.True(t, ok)
	require.Equal(t, []int{1000, 1001}, []int{hdr.GetUID(), hdr.GetGID()})
}
