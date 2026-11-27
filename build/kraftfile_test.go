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

func TestKraftfileToBuildOptsAbsoluteSources(t *testing.T) {
	abs := t.TempDir()
	kernelPath := filepath.Join(abs, "kernel")
	require.NoError(t, os.WriteFile(kernelPath, []byte("kernel"), 0o644))
	romPath := filepath.Join(abs, "romdir")
	require.NoError(t, os.Mkdir(romPath, 0o755))
	rootfsPath := filepath.Join(abs, "rootfs")
	require.NoError(t, os.Mkdir(rootfsPath, 0o755))

	kf := &kraftfile.Kraftfile{
		Kernel: &kraftfile.Kernel{Source: kernelPath},
		Rootfs: &kraftfile.FS{
			Source: &kraftfile.FSSource{Path: rootfsPath},
		},
		Roms: []kraftfile.FS{
			{Source: &kraftfile.FSSource{Path: romPath}},
		},
	}

	opts, err := KraftfileBuildOpts(kf, LocalSource{Dir: t.TempDir()})
	require.NoError(t, err)
	require.Equal(t, kernelPath, opts.Kernel)
	require.Equal(t, rootfsPath, opts.Rootfs.Path)
	require.Len(t, opts.Roms, 1)
	require.Equal(t, romPath, opts.Roms[0].Path)
}

func TestKraftfileToBuildOptsLLBSources(t *testing.T) {
	kf := &kraftfile.Kraftfile{
		Rootfs: &kraftfile.FS{
			Source: &kraftfile.FSSource{Path: "./app/Dockerfile"},
		},
		Roms: []kraftfile.FS{
			{Source: &kraftfile.FSSource{Path: "./public/"}},
		},
	}

	for name, src := range map[string]Source{
		"state":   StateSource{State: llb.Scratch()},
		"session": SessionSource{Name: "context"},
	} {
		t.Run(name, func(t *testing.T) {
			opts, err := KraftfileBuildOpts(kf, src)
			require.NoError(t, err)
			require.IsType(t, src, opts.Rootfs.Source)
			require.Equal(t, "app/Dockerfile", opts.Rootfs.Path)
			require.Equal(t, kraftfile.SourceTypeDockerfile, opts.Rootfs.Type)
			require.Len(t, opts.Roms, 1)
			require.IsType(t, src, opts.Roms[0].Source)
			require.Equal(t, "public", opts.Roms[0].Path)
			require.Equal(t, kraftfile.SourceTypeDirectory, opts.Roms[0].Type)
		})
	}
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

func TestKraftfileBuildOptsKernel(t *testing.T) {
	dir := t.TempDir()
	kf := &kraftfile.Kraftfile{
		Kernel: &kraftfile.Kernel{
			Type:   kraftfile.KernelTypeKernel,
			Source: "build/helloworld_kraftcloud-x86_64",
			KConfig: kraftfile.Map{
				{Key: "CONFIG_LIBUKFS_EROFS", Value: "y"},
			},
		},
		Targets: []kraftfile.Target{
			{
				Arch:    "x86_64",
				Plat:    "kraftcloud",
				KConfig: kraftfile.Map{{Key: "CONFIG_DEBUG", Value: "y"}},
			},
		},
	}

	opts, err := KraftfileBuildOpts(kf, LocalSource{Dir: dir})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "build/helloworld_kraftcloud-x86_64"), opts.Kernel)
	require.Empty(t, opts.Runtime)
	require.Len(t, opts.Platform, 1)
	require.Equal(t, []string{
		"CONFIG_LIBUKFS_EROFS=y",
		"CONFIG_DEBUG=y",
	}, opts.Platform[0].OSFeatures)
	require.Equal(t, kraftfile.FsTypeErofs, defaultRootfsFormat(opts))
}

func TestKraftfileBuildOptsKernelTargetOverride(t *testing.T) {
	kf := &kraftfile.Kraftfile{
		Kernel: &kraftfile.Kernel{
			Type:   kraftfile.KernelTypeKernel,
			Source: "kernel",
			KConfig: kraftfile.Map{
				{Key: "CONFIG_LIBUKFS_EROFS", Value: "y"},
				{Key: "CONFIG_UK_FULLVERSION", Value: "1.0.0"},
			},
		},
		Targets: []kraftfile.Target{
			{
				Arch: "x86_64",
				Plat: "kraftcloud",
				KConfig: kraftfile.Map{
					{Key: "CONFIG_LIBUKFS_EROFS", Value: "n"},
					{Key: "CONFIG_UK_FULLVERSION", Value: "2.0.0"},
				},
			},
		},
	}

	opts, err := KraftfileBuildOpts(kf, LocalSource{Dir: t.TempDir()})
	require.NoError(t, err)
	require.Len(t, opts.Platform, 1)
	require.Equal(t, "2.0.0", opts.Platform[0].OSVersion)
	require.Equal(t, []string{
		"CONFIG_LIBUKFS_EROFS=n",
		"CONFIG_UK_FULLVERSION=2.0.0",
	}, opts.Platform[0].OSFeatures)
	require.Equal(t, kraftfile.FsTypeCpio, defaultRootfsFormat(opts))
}

func TestKraftfileBuildOptsKernelToolingTarget(t *testing.T) {
	kf := &kraftfile.Kraftfile{
		Kernel: &kraftfile.Kernel{
			Type:   kraftfile.KernelTypeKernel,
			Source: "kernel",
			KConfig: kraftfile.Map{
				{Key: "CONFIG_LIBUKFS_EROFS", Value: "y"},
				{Key: "CONFIG_UK_FULLVERSION", Value: "1.0.0"},
			},
		},
	}

	opts, err := KraftfileBuildOpts(kf, LocalSource{Dir: t.TempDir()})
	require.NoError(t, err)
	require.Empty(t, opts.Platform)

	require.NoError(t, opts.FilterArch("amd64"))
	require.Len(t, opts.Platform, 1)
	require.Equal(t, "x86_64", opts.Platform[0].Architecture)
	require.Equal(t, "1.0.0", opts.Platform[0].OSVersion)
	require.Equal(t, []string{
		"CONFIG_LIBUKFS_EROFS=y",
		"CONFIG_UK_FULLVERSION=1.0.0",
	}, opts.Platform[0].OSFeatures)
	require.Equal(t, kraftfile.FsTypeErofs, defaultRootfsFormat(opts))
}

func TestKraftfileBuildOptsKernelInferred(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "vmlinux"), []byte("kernel"), 0o644))
	kf := &kraftfile.Kraftfile{
		Kernel:  &kraftfile.Kernel{Source: "vmlinux"},
		Targets: []kraftfile.Target{{Arch: "x86_64", Plat: "kraftcloud"}},
	}

	opts, err := KraftfileBuildOpts(kf, LocalSource{Dir: dir})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "vmlinux"), opts.Kernel)
}

func TestKraftfileBuildOptsKernelErrors(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "unikraft"), 0o755))
	runtime := kraftfile.Runtime("unikraft.io/unikraft.org/base")

	for name, tc := range map[string]struct {
		kf  *kraftfile.Kraftfile
		src Source
		err string
	}{
		"with runtime": {
			kf: &kraftfile.Kraftfile{
				Runtime: &runtime,
				Kernel:  &kraftfile.Kernel{Type: kraftfile.KernelTypeKernel, Source: "kernel"},
			},
			err: "mutually exclusive",
		},
		"with version": {
			kf: &kraftfile.Kraftfile{
				Kernel: &kraftfile.Kernel{Type: kraftfile.KernelTypeKernel, Source: "kernel", Version: "stable"},
			},
			err: "does not support a version",
		},
		"without source": {
			kf: &kraftfile.Kraftfile{
				Kernel: &kraftfile.Kernel{Type: kraftfile.KernelTypeKernel},
			},
			err: "missing a source path",
		},
		"source version": {
			kf: &kraftfile.Kraftfile{
				Kernel: &kraftfile.Kernel{Version: "stable"},
			},
			err: "building unikraft from source not currently supported",
		},
		"source url": {
			kf: &kraftfile.Kraftfile{
				Kernel: &kraftfile.Kernel{Source: "https://github.com/unikraft/unikraft.git"},
			},
			err: "building unikraft from source not currently supported",
		},
		"source directory": {
			kf: &kraftfile.Kraftfile{
				Kernel: &kraftfile.Kernel{Source: "unikraft"},
			},
			err: "building unikraft from source not currently supported",
		},
		"missing": {
			kf: &kraftfile.Kraftfile{
				Kernel: &kraftfile.Kernel{Source: "absent"},
			},
			err: "kernel path does not exist",
		},
		"state source": {
			kf: &kraftfile.Kraftfile{
				Kernel: &kraftfile.Kernel{Type: kraftfile.KernelTypeKernel, Source: "kernel"},
			},
			src: StateSource{State: llb.Scratch()},
			err: "kernel cannot come from an LLB state",
		},
		"session source": {
			kf: &kraftfile.Kraftfile{
				Kernel: &kraftfile.Kernel{Type: kraftfile.KernelTypeKernel, Source: "kernel"},
			},
			src: SessionSource{Name: "context"},
			err: "kernel cannot come from a session",
		},
	} {
		t.Run(name, func(t *testing.T) {
			src := tc.src
			if src == nil {
				src = LocalSource{Dir: dir}
			}
			_, err := KraftfileBuildOpts(tc.kf, src)
			require.ErrorContains(t, err, tc.err)
		})
	}
}

func TestKraftfileBuildOptsNoFullVersion(t *testing.T) {
	runtime := kraftfile.Runtime("unikraft.io/unikraft.org/base")
	kf := &kraftfile.Kraftfile{
		Runtime: &runtime,
		Targets: []kraftfile.Target{{Arch: "x86_64", Plat: "kraftcloud"}},
	}

	opts, err := KraftfileBuildOpts(kf, LocalSource{})
	require.NoError(t, err)
	require.Len(t, opts.Platform, 1)
	require.Empty(t, opts.Platform[0].OSVersion)
}
