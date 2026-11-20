// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/platforms"
	"github.com/docker/cli/cli/config"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
	"github.com/tonistiigi/fsutil/types"
	goerofs "github.com/unikraft/go-archivefs/erofs"
	"unikraft.com/x/dockerfile"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/image-spec/reference"
	"unikraft.com/x/image-spec/schemes"
	"unikraft.com/x/kraftfile"

	"unikraft.com/x/build/registryauth"
)

//go:embed testdata/busybox.Dockerfile
var busyboxDockerfile []byte

var busybox = dockerfile.MustImage(busyboxDockerfile)

// TestPackInSolve builds a Dockerfile rootfs on the buildkitd at BUILDKIT_HOST,
// packed by the packer of cmd/packer.
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
		Progress:  rec,
		TempDir:   t.TempDir(),
		PackerDir: packerDir(t),
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

	opts.NoCache = true
	rec = &progressRecorder{}
	b.Progress = rec
	build()
	for _, name := range rec.steps() {
		assert.False(t, rec.ended(name).Cached, "%s is not cached without the cache", name)
	}
	opts.NoCache = false

	b.MaxRootfsSize = 1
	_, err = b.BuildRootfs(t.Context(), opts)
	require.ErrorIs(t, err, ErrTooLarge, "the packer's refusal comes back as ErrTooLarge")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM "+busybox+"\nRUN exit 3\n"), 0o644))
	_, err = b.BuildRootfs(t.Context(), opts)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrTooLarge, "a step that exits as the packer refuses is not ErrTooLarge")

	// Symlinks hold no regular bytes, thus only the limit on the export refuses them.
	// XFS limits a target to 1024 bytes.
	links := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(links, "Dockerfile"), []byte("FROM scratch\nCOPY . /\n"), 0o644))
	for i := range 2000 {
		require.NoError(t, os.Symlink(strings.Repeat("x", 1000), filepath.Join(links, strconv.Itoa(i))))
	}
	b.MaxRootfsSize = 1 << 20
	opts.Rootfs.Path = filepath.Join(links, "Dockerfile")
	_, err = b.BuildRootfs(t.Context(), opts)
	require.ErrorIs(t, err, ErrTooLarge, "a packed rootfs over the limit is ErrTooLarge")
}

// TestDockerfileIsDatedByItsBase builds an image from a Dockerfile on the
// buildkitd at BUILDKIT_HOST, and expects the date of its base image.
func TestDockerfileIsDatedByItsBase(t *testing.T) {
	addr := os.Getenv("BUILDKIT_HOST")
	if addr == "" {
		t.Skip("BUILDKIT_HOST must name a buildkitd")
	}
	c, err := client.New(t.Context(), addr)
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM "+busybox+"\n"), 0o644))

	b := Builder{
		Buildkit: func(context.Context) (*client.Client, func(), error) { return c, nil, nil },
		Session: func(context.Context) ([]session.Attachable, error) {
			creds := registryauth.FromDockerConfig(config.LoadDefaultConfigFile(io.Discard))
			return []session.Attachable{creds.Attachable()}, nil
		},
		TempDir: t.TempDir(),
	}
	start := time.Now()
	imgs, err := b.Build(t.Context(), BuildOpts{
		Rootfs:   FSOpts{Path: filepath.Join(dir, "Dockerfile"), Type: kraftfile.SourceTypeDockerfile, Format: kraftfile.FsTypeErofs},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.NoError(t, err)
	closeImages(t, imgs)
	require.Len(t, imgs, 1)

	created := imgs[0].Metadata().Created
	require.NotNil(t, created)
	assert.True(t, created.Before(start), "a Dockerfile must date the image by its base, not now: got %s", created)
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

	packers := map[string]Builder{
		"on the caller": {},
		"in the solve":  {PackerDir: packerDir(t)},
	}
	for where, b := range packers {
		b.Buildkit = func(context.Context) (*client.Client, func(), error) { return c, nil, nil }
		b.Session = func(context.Context) ([]session.Attachable, error) {
			creds := registryauth.FromDockerConfig(config.LoadDefaultConfigFile(io.Discard))
			return []session.Attachable{creds.Attachable(), filesync.NewFSSyncProvider(filesync.StaticDirSource{"context": host})}, nil
		}
		b.TempDir = t.TempDir()
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

// packerDir builds cmd/packer into a Builder.PackerDir for the platform of the host.
func packerDir(t *testing.T) string {
	dir := t.TempDir()
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "linux-"+runtime.GOARCH, "pack"), "./cmd/packer")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return dir
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

	packers := map[string]string{"on the caller": "", "in the solve": packerDir(t)}
	for where, dir := range packers {
		t.Run("packed "+where, func(t *testing.T) {
			b := Builder{
				Buildkit: func(context.Context) (*client.Client, func(), error) { return c, nil, nil },
				Session: func(context.Context) ([]session.Attachable, error) {
					creds := registryauth.FromDockerConfig(config.LoadDefaultConfigFile(io.Discard))
					return []session.Attachable{creds.Attachable()}, nil
				},
				TempDir:   t.TempDir(),
				PackerDir: dir,
			}
			imgs, err := b.BuildRootfs(t.Context(), BuildOpts{
				Rootfs:   FSOpts{Path: busybox, Type: kraftfile.SourceTypeOCI, Format: kraftfile.FsTypeErofs},
				Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}, {OS: "qemu", Architecture: "x86_64"}},
			})
			require.NoError(t, err)
			closeImages(t, imgs)
			require.Len(t, imgs, 2)
			assert.Same(t, imgs[0].Initrd, imgs[1].Initrd, "the second platform shares the pack of the first")
		})
	}
}

// TestPushInSolve builds a Dockerfile rootfs on the buildkitd at BUILDKIT_HOST
// and has the packer of cmd/packer push the image to PUSH_REGISTRY, which this
// test and the steps of the daemon both reach over plain HTTP.
func TestPushInSolve(t *testing.T) {
	addr, reg := os.Getenv("BUILDKIT_HOST"), os.Getenv("PUSH_REGISTRY")
	if addr == "" || reg == "" {
		t.Skip("BUILDKIT_HOST and PUSH_REGISTRY must name a buildkitd and a registry")
	}
	c, err := client.New(t.Context(), addr)
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })

	resolver := docker.NewResolver(docker.ResolverOptions{
		Hosts: docker.ConfigureDefaultRegistries(docker.WithPlainHTTP(docker.MatchAllHosts)),
	})
	access := imagespec.NewAccessor(imagespec.WithResolver(resolver))
	fc := ocispec.Platform{OS: "fc", Architecture: "x86_64"}
	runtime, err := reference.ParseNormalizedNamed(reg + "/runtime:v1")
	require.NoError(t, err)
	_, _, err = imagespec.SaveRegistryImage(t.Context(), runtime, resolver, imagespec.NewImage(
		imagespec.WithPlatform(fc),
		imagespec.WithKernel(imagespec.NewStaticFile("kernel", []byte("kernel"))),
	))
	require.NoError(t, err)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\nCOPY hello /\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello"), []byte("hello\n"), 0o644))

	b := Builder{
		Images:   func(context.Context) (*imagespec.Accessor, error) { return access, nil },
		Buildkit: func(context.Context) (*client.Client, func(), error) { return c, nil, nil },
		Session: func(context.Context) ([]session.Attachable, error) {
			creds := registryauth.FromDockerConfig(config.LoadDefaultConfigFile(io.Discard))
			return []session.Attachable{creds.Attachable()}, nil
		},
		TempDir:   t.TempDir(),
		PackerDir: packerDir(t),
		// No credentials, though the packer asks for them through the socket.
		Credentials: func(string) (string, string, error) { return "", "", nil },
	}
	dest := &imagespec.Location{Scheme: schemes.OCI, Path: reg + "/app:v1"}
	imgs, err := b.Build(t.Context(), BuildOpts{
		Runtime:  runtime.String(),
		Rootfs:   FSOpts{Path: filepath.Join(dir, "Dockerfile"), Type: kraftfile.SourceTypeDockerfile, Format: kraftfile.FsTypeErofs},
		Platform: []ocispec.Platform{fc},
		Cmd:      []string{"/hello"},
		Push:     []*imagespec.Location{dest},
	})
	require.NoError(t, err)
	closeImages(t, imgs)
	require.Len(t, imgs, 1)
	require.Nil(t, imgs[0].Initrd, "the initrd stays on the builder")

	_, err = b.Save(t.Context(), dest, imgs...)
	require.NoError(t, err)

	loaded, err := access.LoadAll(t.Context(), imagespec.Resolved{Location: *dest}, platforms.All)
	require.NoError(t, err)
	closeImages(t, loaded)
	require.Len(t, loaded, 1)
	assert.Equal(t, []string{"/hello"}, loaded[0].Image.Config.Cmd)

	rc, _, err := loaded[0].Initrd.Open(t.Context())
	require.NoError(t, err)
	defer rc.Close()
	initrd, err := io.ReadAll(rc)
	require.NoError(t, err)
	fsys, err := goerofs.Open(bytes.NewReader(initrd))
	require.NoError(t, err)
	hello, err := fs.ReadFile(fsys, "hello")
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(hello))
}

// TestPushFailsInSolve builds for two platforms as TestPushInSolve does, with
// a Dockerfile that only the first one passes.
func TestPushFailsInSolve(t *testing.T) {
	addr, reg := os.Getenv("BUILDKIT_HOST"), os.Getenv("PUSH_REGISTRY")
	if addr == "" || reg == "" {
		t.Skip("BUILDKIT_HOST and PUSH_REGISTRY must name a buildkitd and a registry")
	}
	c, err := client.New(t.Context(), addr)
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })

	resolver := docker.NewResolver(docker.ResolverOptions{
		Hosts: docker.ConfigureDefaultRegistries(docker.WithPlainHTTP(docker.MatchAllHosts)),
	})
	host, other := ocispec.Platform{OS: "fc", Architecture: "x86_64"}, ocispec.Platform{OS: "fc", Architecture: "arm64"}
	if runtime.GOARCH == "arm64" {
		host, other = other, host
	}
	named, err := reference.ParseNormalizedNamed(reg + "/runtime:v2")
	require.NoError(t, err)
	_, _, err = imagespec.SaveRegistryImage(t.Context(), named, resolver,
		imagespec.NewImage(imagespec.WithPlatform(host), imagespec.WithKernel(imagespec.NewStaticFile("kernel", []byte("kernel")))),
		imagespec.NewImage(imagespec.WithPlatform(other), imagespec.WithKernel(imagespec.NewStaticFile("kernel", []byte("kernel")))),
	)
	require.NoError(t, err)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(
		"FROM --platform=$BUILDPLATFORM "+busybox+" AS check\nARG TARGETARCH\nRUN [ $TARGETARCH = "+runtime.GOARCH+" ] && date >/built\n"+
			"FROM scratch\nCOPY --from=check /built /\n"), 0o644))

	b := Builder{
		Images: func(context.Context) (*imagespec.Accessor, error) {
			return imagespec.NewAccessor(imagespec.WithResolver(resolver)), nil
		},
		Buildkit: func(context.Context) (*client.Client, func(), error) { return c, nil, nil },
		Session: func(context.Context) ([]session.Attachable, error) {
			creds := registryauth.FromDockerConfig(config.LoadDefaultConfigFile(io.Discard))
			return []session.Attachable{creds.Attachable()}, nil
		},
		TempDir:     t.TempDir(),
		PackerDir:   packerDir(t),
		Credentials: func(string) (string, string, error) { return "", "", nil },
	}
	rec := &progressRecorder{}
	b.Progress = rec
	repo := "failed-" + identity.NewID()[:12] // a tag of an earlier run would stay
	_, err = b.Build(t.Context(), BuildOpts{
		Runtime:  named.String(),
		Rootfs:   FSOpts{Path: filepath.Join(dir, "Dockerfile"), Type: kraftfile.SourceTypeDockerfile, Format: kraftfile.FsTypeErofs},
		Platform: []ocispec.Platform{host, other},
		Push:     []*imagespec.Location{{Scheme: schemes.OCI, Path: reg + "/" + repo + ":v1"}},
	})
	require.Error(t, err)
	require.True(t, slices.ContainsFunc(rec.steps(), func(name string) bool {
		return strings.Contains(name, platforms.Format(host)) && strings.Contains(name, "push")
	}), "the first platform was pushed: %v", rec.steps())

	resp, err := http.Get("http://" + reg + "/v2/" + repo + "/tags/list")
	require.NoError(t, err)
	defer resp.Body.Close()
	var list struct{ Tags []string }
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
	assert.Empty(t, list.Tags, "the temporary tag of the first platform is gone")
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
