// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
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
