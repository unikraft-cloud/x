// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/session"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/kraftfile"
)

func TestRootfsOCINilAccessor(t *testing.T) {
	_, err := Builder{
		Images: func(context.Context) (*imagespec.Accessor, error) {
			return nil, nil
		},
	}.BuildRootfs(t.Context(), BuildOpts{
		Rootfs: FSOpts{
			Path: "unikraft.io/official/base-compat",
			Type: kraftfile.SourceTypeOCI,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.ErrorContains(t, err, "image callback gave no accessor")
}

func TestRootfsDockerfileWithoutBuildkit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Dockerfile")
	require.NoError(t, os.WriteFile(path, []byte("FROM scratch\n"), 0o644))

	_, err := Builder{}.BuildRootfs(t.Context(), BuildOpts{
		Rootfs: FSOpts{
			Path:   path,
			Type:   kraftfile.SourceTypeDockerfile,
			Format: kraftfile.FsTypeCpio,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.ErrorContains(t, err, "no buildkit connector configured")
}

// TestBuildLLBSourcesNeedBuildkit asserts that the sources in a StateSource or
// a SessionSource are not read from the host.
func TestBuildLLBSourcesNeedBuildkit(t *testing.T) {
	t.Chdir(writeTestDirectory(t))
	st := StateSource{State: llb.Scratch()}

	for name, opts := range map[string]BuildOpts{
		"dir rom": {
			Roms: []FSOpts{{Source: st, Path: "subdir", Type: kraftfile.SourceTypeDirectory}},
		},
		"dockerfile rootfs": {
			Rootfs: FSOpts{Source: st, Path: "Dockerfile", Type: kraftfile.SourceTypeDockerfile},
		},
		"session dir rom": {
			Roms: []FSOpts{{Source: SessionSource{Name: "context"}, Path: "subdir", Type: kraftfile.SourceTypeDirectory}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			opts.Platform = []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}}
			_, err := Builder{}.Build(t.Context(), opts)
			require.ErrorContains(t, err, "no buildkit connector configured")
		})
	}
}

func TestRootfsDockerfileSessionError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Dockerfile")
	require.NoError(t, os.WriteFile(path, []byte("FROM scratch\n"), 0o644))

	_, err := Builder{
		Buildkit: func(context.Context) (*client.Client, func(), error) {
			return nil, nil, errors.New("buildkit called")
		},
		Session: func(context.Context) ([]session.Attachable, error) {
			return nil, errors.New("no profile")
		},
	}.BuildRootfs(t.Context(), BuildOpts{
		Rootfs: FSOpts{
			Path:   path,
			Type:   kraftfile.SourceTypeDockerfile,
			Format: kraftfile.FsTypeCpio,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.ErrorContains(t, err, "no profile")
}

func TestRootfsLocalSourcesSkipDependencies(t *testing.T) {
	b := Builder{
		Images: func(context.Context) (*imagespec.Accessor, error) {
			return nil, errors.New("images called")
		},
		Buildkit: func(context.Context) (*client.Client, func(), error) {
			return nil, nil, errors.New("buildkit called")
		},
	}

	for _, src := range []FSOpts{
		{Path: writeTestDirectory(t), Type: kraftfile.SourceTypeDirectory},
		{Path: writeTestTarballFile(t), Type: kraftfile.SourceTypeTarball},
		{Path: writeTestCpioFile(t), Type: kraftfile.SourceTypeCpio},
	} {
		t.Run(string(src.Type), func(t *testing.T) {
			imgs, err := b.BuildRootfs(t.Context(), BuildOpts{
				Rootfs:   src,
				Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
			})
			require.NoError(t, err)
			for _, img := range imgs {
				_ = img.Close()
			}
		})
	}
}

func TestRootfsTempDir(t *testing.T) {
	dir := t.TempDir()
	imgs, err := Builder{TempDir: dir}.BuildRootfs(t.Context(), BuildOpts{
		Rootfs: FSOpts{
			Path:   writeTestDirectory(t),
			Type:   kraftfile.SourceTypeDirectory,
			Format: kraftfile.FsTypeCpio,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, img := range imgs {
			_ = img.Close()
		}
	})

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

// progressRecorder keeps every vertex reported to it, in order.
type progressRecorder struct {
	mu       sync.Mutex
	vertices []*client.Vertex
}

func (r *progressRecorder) Write(ss *client.SolveStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.vertices = append(r.vertices, ss.Vertexes...)
}

// steps returns the name of each step reported, in the order they started.
func (r *progressRecorder) steps() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var names []string
	seen := map[digest.Digest]bool{}
	for _, v := range r.vertices {
		if !seen[v.Digest] {
			seen[v.Digest] = true
			names = append(names, v.Name)
		}
	}
	return names
}

// ended returns how the step named name was reported to end, or nil when it
// was not.
func (r *progressRecorder) ended(name string) *client.Vertex {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, v := range slices.Backward(r.vertices) {
		if v.Name == name && v.Completed != nil {
			return v
		}
	}
	return nil
}

// closeImages closes imgs once the test is done.
func closeImages(t *testing.T, imgs []*imagespec.Image) {
	t.Helper()
	t.Cleanup(func() {
		for _, img := range imgs {
			_ = img.Close()
		}
	})
}

func TestProgressPackIsAStepPerPlatform(t *testing.T) {
	var rec progressRecorder
	imgs, err := Builder{Progress: &rec}.BuildRootfs(t.Context(), BuildOpts{
		Rootfs: FSOpts{
			Path:   writeTestDirectory(t),
			Type:   kraftfile.SourceTypeDirectory,
			Format: kraftfile.FsTypeCpio,
		},
		Platform: []ocispec.Platform{
			{OS: "fc", Architecture: "x86_64"},
			{OS: "fc", Architecture: "arm64"},
		},
	})
	require.NoError(t, err)
	closeImages(t, imgs)

	require.Equal(t, []string{
		"[pack fc/x86_64] rootfs as cpio",
		"[pack fc/arm64] rootfs as cpio",
	}, rec.steps())
}

func TestProgressRomsAndRootfsArePackedUnderTheirNames(t *testing.T) {
	dir := writeTestDirectory(t)
	fs := FSOpts{Path: dir, Type: kraftfile.SourceTypeDirectory, Format: kraftfile.FsTypeErofs}

	var rec progressRecorder
	imgs, err := Builder{Progress: &rec}.Build(t.Context(), BuildOpts{
		Rootfs:   fs,
		Roms:     []FSOpts{fs, fs},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.NoError(t, err)
	closeImages(t, imgs)

	require.Equal(t, []string{
		"[pack fc/x86_64] rom 1 as erofs",
		"[pack fc/x86_64] rom 2 as erofs",
		"[pack fc/x86_64] rootfs as erofs",
	}, rec.steps())
}

func TestProgressUnikraftImageIsLoadedAndItsInitrdCopied(t *testing.T) {
	path := "oci-archive://" + writeUnikraftOCIArchive(t, writeTestDirectory(t))

	var rec progressRecorder
	imgs, err := Builder{Progress: &rec}.BuildRootfs(t.Context(), BuildOpts{
		Rootfs:   FSOpts{Path: path, Type: kraftfile.SourceTypeOCI},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.NoError(t, err)
	closeImages(t, imgs)

	require.Equal(t, []string{
		"[rootfs] load " + path,
		"[rootfs fc/x86_64] copy initrd of " + path,
	}, rec.steps())
	copied := rec.ended("[rootfs fc/x86_64] copy initrd of " + path)
	require.NotNil(t, copied)
	assert.Empty(t, copied.Error)
}
