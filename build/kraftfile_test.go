// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/kraftfile"
)

func TestKraftfileToBuildOpts(t *testing.T) {
	rootfsDir := t.TempDir()
	rootfsPath := "Dockerfile"

	runtime := kraftfile.Runtime("unikraft.io/unikraft.org/base")
	kf := &kraftfile.Kraftfile{
		Cmd:     kraftfile.Command{"/server", "--flag"},
		Env:     kraftfile.Map{{Key: "A", Value: "1"}},
		Labels:  map[string]string{"label": "value"},
		Runtime: &runtime,
		Rootfs: &kraftfile.FS{
			Format: kraftfile.FsTypeErofs,
			Source: &kraftfile.FSSource{
				Path: rootfsPath,
			},
		},
		Targets: []kraftfile.Target{
			{
				Arch: "x86_64",
				Plat: "fc",
				KConfig: kraftfile.Map{
					{Key: "CONFIG_UK_FULLVERSION", Value: "1.2.3"},
					{Key: "CONFIG_DEBUG", Value: "y"},
				},
			},
		},
	}

	opts, err := KraftfileBuildOpts(kf, LocalSource{Dir: rootfsDir})
	require.NoError(t, err)
	require.Equal(t, []string{"/server", "--flag"}, opts.Cmd)
	require.Equal(t, kraftfile.Map{{Key: "A", Value: "1"}}, opts.Env)
	require.Equal(t, map[string]string{"label": "value"}, opts.Labels)
	require.Equal(t, "unikraft.io/unikraft.org/base", opts.Runtime)
	require.Equal(t, kraftfile.FsTypeErofs, opts.Rootfs.Format)
	require.Equal(t, filepath.Join(rootfsDir, "Dockerfile"), opts.Rootfs.Path)
	require.Equal(t, kraftfile.SourceTypeDockerfile, opts.Rootfs.Type)
	require.Len(t, opts.Platform, 1)
	require.Equal(t, "x86_64", opts.Platform[0].Architecture)
	require.Equal(t, "fc", opts.Platform[0].OS)
	require.Equal(t, "1.2.3", opts.Platform[0].OSVersion)
	require.Equal(t, []string{
		"CONFIG_UK_FULLVERSION=1.2.3",
		"CONFIG_DEBUG=y",
	}, opts.Platform[0].OSFeatures)
}

// TestKraftfileToBuildOptsResolvesSources asserts that every source leaves here
// resolved against the kraftfile directory and typed.
func TestKraftfileToBuildOptsResolvesSources(t *testing.T) {
	rootfsDir := t.TempDir()
	romPath := filepath.Join(rootfsDir, "romdir")
	require.NoError(t, os.Mkdir(romPath, 0o755))

	runtime := kraftfile.Runtime("unikraft.io/unikraft.org/base")
	kf := &kraftfile.Kraftfile{
		Runtime: &runtime,
		Rootfs: &kraftfile.FS{
			Format: kraftfile.FsTypeErofs,
			Source: &kraftfile.FSSource{
				Path: "Dockerfile",
			},
		},
		Roms: []kraftfile.FS{
			{Source: &kraftfile.FSSource{Path: "romdir"}},
		},
	}

	opts, err := KraftfileBuildOpts(kf, LocalSource{Dir: rootfsDir})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(rootfsDir, "Dockerfile"), opts.Rootfs.Path)
	require.Equal(t, kraftfile.SourceTypeDockerfile, opts.Rootfs.Type)
	require.Len(t, opts.Roms, 1)
	require.Equal(t, romPath, opts.Roms[0].Path)
	require.Equal(t, kraftfile.SourceTypeDirectory, opts.Roms[0].Type)
}

func TestKraftfileToBuildOptsStateSource(t *testing.T) {
	kf := &kraftfile.Kraftfile{
		Rootfs: &kraftfile.FS{
			Source: &kraftfile.FSSource{Path: "./app/Dockerfile"},
		},
		Roms: []kraftfile.FS{
			{Source: &kraftfile.FSSource{Path: "./public/"}},
		},
	}

	opts, err := KraftfileBuildOpts(kf, StateSource{State: llb.Scratch()})
	require.NoError(t, err)
	require.IsType(t, StateSource{}, opts.Rootfs.Source)
	require.Equal(t, "app/Dockerfile", opts.Rootfs.Path)
	require.Equal(t, kraftfile.SourceTypeDockerfile, opts.Rootfs.Type)
	require.Len(t, opts.Roms, 1)
	require.IsType(t, StateSource{}, opts.Roms[0].Source)
	require.Equal(t, "public", opts.Roms[0].Path)
	require.Equal(t, kraftfile.SourceTypeDirectory, opts.Roms[0].Type)
}

// TestKraftfileToBuildOptsMissingSource asserts the fail-fast that resolving
// here buys: a bad path is reported before anything connects to BuildKit.
func TestKraftfileToBuildOptsMissingSource(t *testing.T) {
	runtime := kraftfile.Runtime("unikraft.io/unikraft.org/base")
	kf := &kraftfile.Kraftfile{
		Runtime: &runtime,
		Rootfs: &kraftfile.FS{
			Source: &kraftfile.FSSource{Path: "rootfs.tar"},
		},
	}

	_, err := KraftfileBuildOpts(kf, LocalSource{Dir: t.TempDir()})
	require.ErrorIs(t, err, fs.ErrNotExist)
	require.ErrorContains(t, err, "resolving rootfs source")
}

func TestKraftfileToBuildOptsDockerfileWithType(t *testing.T) {
	rootfsDir := t.TempDir()

	runtime := kraftfile.Runtime("unikraft.io/unikraft.org/base")
	kf := &kraftfile.Kraftfile{
		Runtime: &runtime,
		Rootfs: &kraftfile.FS{
			Format: kraftfile.FsTypeErofs,
			Source: &kraftfile.FSSource{
				Path:       "context",
				Dockerfile: "MyDockerfile",
				Type:       kraftfile.SourceTypeDockerfile,
			},
		},
	}

	opts, err := KraftfileBuildOpts(kf, LocalSource{Dir: rootfsDir})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(rootfsDir, "context"), opts.Rootfs.Path)
	require.Equal(t, "MyDockerfile", opts.Rootfs.Dockerfile)
	require.Equal(t, kraftfile.SourceTypeDockerfile, opts.Rootfs.Type)
	require.Equal(t, kraftfile.FsTypeErofs, opts.Rootfs.Format)
}

func TestKraftfileToBuildOptsRomDockerfile(t *testing.T) {
	rootfsDir := t.TempDir()

	runtime := kraftfile.Runtime("unikraft.io/unikraft.org/base")
	kf := &kraftfile.Kraftfile{
		Runtime: &runtime,
		Roms: []kraftfile.FS{
			{Source: &kraftfile.FSSource{Path: "rom", Dockerfile: "Dockerfile.rom"}},
		},
	}

	opts, err := KraftfileBuildOpts(kf, LocalSource{Dir: rootfsDir})
	require.NoError(t, err)
	require.Len(t, opts.Roms, 1)
	require.Equal(t, filepath.Join(rootfsDir, "rom"), opts.Roms[0].Path)
	require.Equal(t, "Dockerfile.rom", opts.Roms[0].Dockerfile)
	require.Equal(t, kraftfile.SourceTypeDockerfile, opts.Roms[0].Type)
}

func TestKraftfileToBuildOptsDockerfileWithoutType(t *testing.T) {
	rootfsDir := t.TempDir()

	runtime := kraftfile.Runtime("unikraft.io/unikraft.org/base")
	kf := &kraftfile.Kraftfile{
		Runtime: &runtime,
		Rootfs: &kraftfile.FS{
			Format: kraftfile.FsTypeErofs,
			Source: &kraftfile.FSSource{
				Path:       "context",
				Dockerfile: "MyDockerfile",
			},
		},
	}

	opts, err := KraftfileBuildOpts(kf, LocalSource{Dir: rootfsDir})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(rootfsDir, "context"), opts.Rootfs.Path)
	require.Equal(t, "MyDockerfile", opts.Rootfs.Dockerfile)
	require.Equal(t, kraftfile.SourceTypeDockerfile, opts.Rootfs.Type,
		"type must be inferred as dockerfile when dockerfile field is set")
	require.Equal(t, kraftfile.FsTypeErofs, opts.Rootfs.Format)
}

func TestKraftfileToBuildOptsNoRootfs(t *testing.T) {
	rootfsDir := t.TempDir()

	runtime := kraftfile.Runtime("unikraft.io/unikraft.org/base")
	kf := &kraftfile.Kraftfile{
		Cmd:     kraftfile.Command{"/server"},
		Runtime: &runtime,
		Targets: []kraftfile.Target{
			{
				Arch: "x86_64",
				Plat: "fc",
			},
		},
	}

	opts, err := KraftfileBuildOpts(kf, LocalSource{Dir: rootfsDir})
	require.NoError(t, err)
	require.Equal(t, "unikraft.io/unikraft.org/base", opts.Runtime)
	require.Empty(t, opts.Rootfs.Path)
	require.False(t, opts.Rootfs.Compress)
	require.Equal(t, []string{"/server"}, opts.Cmd)
	require.Len(t, opts.Platform, 1)
	require.Equal(t, "x86_64", opts.Platform[0].Architecture)
	require.Equal(t, "fc", opts.Platform[0].OS)
}

func TestKraftfileToBuildOptsRootfsOCIType(t *testing.T) {
	rootfsDir := t.TempDir()

	runtime := kraftfile.Runtime("unikraft.io/unikraft.org/base")
	kf := &kraftfile.Kraftfile{
		Runtime: &runtime,
		Rootfs: &kraftfile.FS{
			Format: kraftfile.FsTypeErofs,
			Source: &kraftfile.FSSource{
				Path: "index.docker.io/hello-world:latest",
				Type: kraftfile.SourceTypeOCI,
			},
		},
		Targets: []kraftfile.Target{
			{Arch: "x86_64", Plat: "fc"},
		},
	}

	opts, err := KraftfileBuildOpts(kf, LocalSource{Dir: rootfsDir})
	require.NoError(t, err)
	require.Equal(t, "index.docker.io/hello-world:latest", opts.Rootfs.Path,
		"OCI rootfs reference must not be joined with the kraftfile directory")
	require.Equal(t, kraftfile.SourceTypeOCI, opts.Rootfs.Type)
}
