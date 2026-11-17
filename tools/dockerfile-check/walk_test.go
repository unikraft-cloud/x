// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsDockerfile(t *testing.T) {
	for name, want := range map[string]bool{
		"Dockerfile":              true,
		"Dockerfile.dev":          true,
		"registry.Dockerfile":     true,
		"registry.dockerfile":     true,
		"Containerfile":           true,
		"base.Containerfile":      true,
		"Dockerfile.dockerignore": false,
		"dockerfile.go":           false,
		"dockerfile_test.go":      false,
		"Dockerfiles":             false,
		"README.md":               false,
	} {
		assert.Equal(t, want, isDockerfile(name), name)
	}
}

func TestCollect(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{
		"Dockerfile",
		"a/b/x.Dockerfile",
		"a/main.go",
		"ignored/Dockerfile",
		".git/Dockerfile",
	} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, p), nil, 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored/\n"), 0o644))

	got, err := collect([]string{dir}, true)
	require.NoError(t, err)
	assert.Equal(t, []string{
		filepath.Join(dir, "Dockerfile"),
		filepath.Join(dir, "a/b/x.Dockerfile"),
	}, got)

	got, err = collect([]string{dir}, false)
	require.NoError(t, err)
	assert.Equal(t, []string{
		filepath.Join(dir, "Dockerfile"),
		filepath.Join(dir, "a/b/x.Dockerfile"),
		filepath.Join(dir, "ignored/Dockerfile"),
	}, got)
}
