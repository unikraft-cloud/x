// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package buildfs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func createEROFS(t *testing.T, fsys string) []byte {
	t.Helper()

	path := filepath.Join(t.TempDir(), "rootfs.erofs")
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, CreateEROFS(f, os.DirFS(fsys)))
	require.NoError(t, f.Close())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func TestCreateEROFSIdenticalTreesAreIdentical(t *testing.T) {
	first := createEROFS(t, newTree(t))
	second := createEROFS(t, newTree(t))
	require.Equal(t, first, second, "identical trees in different locations must give identical images")
}
