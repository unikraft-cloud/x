// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/docker/cli/cli/config"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
	"github.com/tonistiigi/fsutil/types"
	goerofs "github.com/unikraft/go-archivefs/erofs"
	"unikraft.com/x/kraftfile"

	"unikraft.com/x/build/registryauth"
)

// TestPackInSolve builds a Dockerfile rootfs on the buildkitd at BUILDKIT_HOST,
// packed by DefaultPacker, which the daemon pulls with the credentials of the
// docker config.
func TestPackInSolve(t *testing.T) {
	addr := os.Getenv("BUILDKIT_HOST")
	if addr == "" {
		t.Skip("BUILDKIT_HOST must name a buildkitd")
	}
	c, err := client.New(t.Context(), addr)
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\nCOPY . /\n"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "etc"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "etc", "hello"), []byte("hello\n"), 0o644))
	require.NoError(t, os.Symlink("/etc/hello", filepath.Join(dir, "hello")))

	rec := &progressRecorder{}
	b := Builder{
		Buildkit: func(context.Context) (*client.Client, func(), error) { return c, nil, nil },
		Session: func(context.Context) ([]session.Attachable, error) {
			creds := registryauth.FromDockerConfig(config.LoadDefaultConfigFile(io.Discard))
			return []session.Attachable{creds.Attachable()}, nil
		},
		Progress: rec,
		TempDir:  t.TempDir(),
		Packer:   DefaultPacker,
	}
	opts := BuildOpts{
		Rootfs:   FSOpts{Path: filepath.Join(dir, "Dockerfile"), Type: kraftfile.SourceTypeDockerfile, Format: kraftfile.FsTypeErofs},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	}

	build := func() {
		imgs, err := b.BuildRootfs(t.Context(), opts)
		require.NoError(t, err)
		require.Len(t, imgs, 1)
		t.Cleanup(func() { imgs[0].Close() })

		rc, _, err := imgs[0].Initrd.Open(t.Context())
		require.NoError(t, err)
		defer rc.Close()
		fsys, err := goerofs.Open(rc.(io.ReaderAt))
		require.NoError(t, err)
		data, err := fs.ReadFile(fsys, "etc/hello")
		require.NoError(t, err)
		assert.Equal(t, "hello\n", string(data))
		target, err := fsys.ReadLink("hello")
		require.NoError(t, err)
		assert.Equal(t, "/etc/hello", target)
	}

	// The first build may hit the cache of an earlier run as well.
	const step = "[rootfs fc/x86_64] pack as erofs"
	build()
	require.NotNil(t, rec.ended(step), "the pack step is in the stream")
	build()
	assert.True(t, rec.ended(step).Cached, "the second pack is cached")

	b.MaxRootfsSize = 1
	_, err = b.BuildRootfs(t.Context(), opts)
	require.ErrorIs(t, err, ErrTooLarge, "the packer's refusal comes back as ErrTooLarge")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM busybox\nRUN exit 3\n"), 0o644))
	_, err = b.BuildRootfs(t.Context(), opts)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrTooLarge, "a step that exits as the packer refuses is not ErrTooLarge")

	// Symlinks hold no regular bytes, thus only the limit on the export refuses them.
	links := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(links, "Dockerfile"), []byte("FROM scratch\nCOPY . /\n"), 0o644))
	for i := range 500 {
		require.NoError(t, os.Symlink(strings.Repeat("x", 4000), filepath.Join(links, strconv.Itoa(i))))
	}
	b.MaxRootfsSize = 1 << 20
	opts.Rootfs.Path = filepath.Join(links, "Dockerfile")
	_, err = b.BuildRootfs(t.Context(), opts)
	require.ErrorIs(t, err, ErrTooLarge, "a packed rootfs over the limit is ErrTooLarge")
}

// TestPackOwners packs a directory of each LLB source built on the buildkitd at
// BUILDKIT_HOST, on the caller and in the solve.
func TestPackOwners(t *testing.T) {
	addr := os.Getenv("BUILDKIT_HOST")
	if addr == "" {
		t.Skip("BUILDKIT_HOST must name a buildkitd")
	}
	c, err := client.New(t.Context(), addr)
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("session\n"), 0o644))
	host, err := fsutil.NewFS(dir)
	require.NoError(t, err)
	host, err = fsutil.NewFilterFS(host, &fsutil.FilterOpt{Map: func(_ string, st *types.Stat) fsutil.MapResult {
		st.Uid, st.Gid = 1000, 1000
		return fsutil.MapResultKeep
	}})
	require.NoError(t, err)

	state := llb.Scratch().File(llb.Mkfile("/file.txt", 0o644, []byte("state\n"), llb.WithUIDGID(1000, 1000)))

	packers := map[string]string{"on the caller": "", "in the solve": DefaultPacker}
	for where, packer := range packers {
		b := Builder{
			Buildkit: func(context.Context) (*client.Client, func(), error) { return c, nil, nil },
			Session: func(context.Context) ([]session.Attachable, error) {
				creds := registryauth.FromDockerConfig(config.LoadDefaultConfigFile(io.Discard))
				return []session.Attachable{creds.Attachable(), filesync.NewFSSyncProvider(filesync.StaticDirSource{"context": host})}, nil
			},
			TempDir: t.TempDir(),
			Packer:  packer,
		}
		ownerOf := func(t *testing.T, src Source) owner {
			imgs, err := b.BuildRootfs(t.Context(), BuildOpts{
				Rootfs:   FSOpts{Path: ".", Type: kraftfile.SourceTypeDirectory, Source: src, Format: kraftfile.FsTypeCpio},
				Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
			})
			require.NoError(t, err)
			closeImages(t, imgs)
			return readOwners(t, imgs[0].Initrd, kraftfile.FsTypeCpio)["file.txt"]
		}

		t.Run("a state keeps its owners when packed "+where, func(t *testing.T) {
			assert.Equal(t, owner{1000, 1000}, ownerOf(t, StateSource{State: state}))
		})
		t.Run("a session directory is owned by root when packed "+where, func(t *testing.T) {
			assert.Equal(t, owner{0, 0}, ownerOf(t, SessionSource{Name: "context"}))
		})
	}
}

// TestPackOncePerSource builds a regular image for two platforms on the
// buildkitd at BUILDKIT_HOST, on the caller and in the solve, and expects
// both to share one pack.
func TestPackOncePerSource(t *testing.T) {
	addr := os.Getenv("BUILDKIT_HOST")
	if addr == "" {
		t.Skip("BUILDKIT_HOST must name a buildkitd")
	}
	c, err := client.New(t.Context(), addr)
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })

	packers := map[string]string{"on the caller": "", "in the solve": DefaultPacker}
	for where, packer := range packers {
		t.Run("packed "+where, func(t *testing.T) {
			b := Builder{
				Buildkit: func(context.Context) (*client.Client, func(), error) { return c, nil, nil },
				Session: func(context.Context) ([]session.Attachable, error) {
					creds := registryauth.FromDockerConfig(config.LoadDefaultConfigFile(io.Discard))
					return []session.Attachable{creds.Attachable()}, nil
				},
				TempDir: t.TempDir(),
				Packer:  packer,
			}
			imgs, err := b.BuildRootfs(t.Context(), BuildOpts{
				Rootfs:   FSOpts{Path: "docker.io/library/busybox:latest", Type: kraftfile.SourceTypeOCI, Format: kraftfile.FsTypeErofs},
				Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}, {OS: "qemu", Architecture: "x86_64"}},
			})
			require.NoError(t, err)
			closeImages(t, imgs)
			require.Len(t, imgs, 2)
			assert.Same(t, imgs[0].Initrd, imgs[1].Initrd, "the second platform shares the pack of the first")
		})
	}
}

func TestPackArgs(t *testing.T) {
	for name, tc := range map[string]struct {
		opts    BuildOpts
		format  kraftfile.FsType
		maxSize int64
		want    []string
	}{
		"a state keeps its owners": {
			opts:    BuildOpts{Rootfs: FSOpts{Type: kraftfile.SourceTypeDirectory, Source: StateSource{}, Pad: 4096, Compress: true}},
			format:  kraftfile.FsTypeCpio,
			maxSize: 1 << 20,
			want:    []string{"/pack", "pack", "--format=cpio", "--pad=4096", "--max-size=1048576", "--compress", "/src", "/out/initrd"},
		},
		"a session directory is owned by root": {
			opts:   BuildOpts{Rootfs: FSOpts{Type: kraftfile.SourceTypeDirectory, Source: SessionSource{Name: "context"}}},
			format: kraftfile.FsTypeErofs,
			want:   []string{"/pack", "pack", "--format=erofs", "--pad=0", "--max-size=0", "--all-root", "/src", "/out/initrd"},
		},
		"a session dockerfile keeps its owners": {
			opts:   BuildOpts{Rootfs: FSOpts{Type: kraftfile.SourceTypeDockerfile, Source: SessionSource{Name: "context"}}},
			format: kraftfile.FsTypeErofs,
			want:   []string{"/pack", "pack", "--format=erofs", "--pad=0", "--max-size=0", "/src", "/out/initrd"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, packArgs(tc.opts, tc.format, tc.maxSize))
		})
	}
}
