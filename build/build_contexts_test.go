// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").

package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyBuildOptsLocalNamedContext(t *testing.T) {
	dir := t.TempDir()
	contextDir := filepath.Join(dir, "shared")
	require.NoError(t, os.Mkdir(contextDir, 0o755))

	attrs := map[string]string{}
	localDirs := map[string]string{}

	err := applyBuildOpts(attrs, localDirs, nil, BuildOpts{
		Rootfs: FSOpts{Path: dir, Dockerfile: "Dockerfile"},
		BuildContexts: map[string]string{
			"shared": contextDir,
		},
	})

	require.NoError(t, err)
	require.Equal(t, "local:shared", attrs["context:shared"])
	require.Equal(t, contextDir, localDirs["shared"])
}

func TestApplyBuildOptsRemoteNamedContext(t *testing.T) {
	attrs := map[string]string{}
	localDirs := map[string]string{}

	err := applyBuildOpts(attrs, localDirs, nil, BuildOpts{
		Rootfs: FSOpts{Path: t.TempDir(), Dockerfile: "Dockerfile"},
		BuildContexts: map[string]string{
			"remote": "https://example.com/context.git",
			"image":  "docker-image://alpine:latest",
			"oci":    "oci-layout:/tmp/layout@sha256:abc",
		},
	})

	require.NoError(t, err)
	require.Equal(t, "https://example.com/context.git", attrs["context:remote"])
	require.Equal(t, "docker-image://alpine:latest", attrs["context:image"])
	require.Equal(t, "oci-layout:/tmp/layout@sha256:abc", attrs["context:oci"])
	require.NotContains(t, localDirs, "remote")
	require.NotContains(t, localDirs, "image")
	require.NotContains(t, localDirs, "oci")
}

func TestApplyBuildOptsNamedContextRequiresDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(file, []byte("test"), 0o600))

	err := applyBuildOpts(map[string]string{}, map[string]string{}, nil, BuildOpts{
		Rootfs: FSOpts{Path: t.TempDir(), Dockerfile: "Dockerfile"},
		BuildContexts: map[string]string{
			"shared": file,
		},
	})

	require.ErrorContains(t, err, "must be a directory")
}

func TestApplyBuildOptsNamedContextRejectsMissingPath(t *testing.T) {
	err := applyBuildOpts(map[string]string{}, map[string]string{}, nil, BuildOpts{
		Rootfs: FSOpts{Path: t.TempDir(), Dockerfile: "Dockerfile"},
		BuildContexts: map[string]string{
			"shared": filepath.Join(t.TempDir(), "missing"),
		},
	})

	require.ErrorContains(t, err, "could not access build context")
}

func TestApplyBuildOptsNamedContextRejectsReservedName(t *testing.T) {
	err := applyBuildOpts(map[string]string{}, map[string]string{}, nil, BuildOpts{
		Rootfs: FSOpts{Path: t.TempDir(), Dockerfile: "Dockerfile"},
		BuildContexts: map[string]string{
			"context": t.TempDir(),
		},
	})

	require.ErrorContains(t, err, "reserved name")
}
