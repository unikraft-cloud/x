// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package buildfs

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unikraft/go-archivefs"
	"github.com/unikraft/go-archivefs/erofs"
	"github.com/unikraft/go-cpio"
)

// readCPIO returns the headers of a CPIO archive in archive order.
func readCPIO(t *testing.T, data []byte) []*cpio.Header {
	t.Helper()

	var headers []*cpio.Header
	r := cpio.NewReader(bytes.NewReader(data))
	for {
		hdr, err := r.Next()
		if errors.Is(err, io.EOF) {
			return headers
		}
		require.NoError(t, err)
		headers = append(headers, hdr)
	}
}

// headerByName returns the header of the entry called name.
func headerByName(t *testing.T, headers []*cpio.Header, name string) *cpio.Header {
	t.Helper()

	for _, hdr := range headers {
		if hdr.Name == name {
			return hdr
		}
	}
	require.Failf(t, "entry not found", "%s", name)
	return nil
}

func createCPIO(t *testing.T, fsys fs.FS, opts ...CreateOption) []byte {
	t.Helper()

	var buf bytes.Buffer
	require.NoError(t, CreateCPIO(t.Context(), &buf, fsys, opts...))
	return buf.Bytes()
}

func TestCreateCPIOInodesFollowWalkOrder(t *testing.T) {
	var tarball bytes.Buffer
	tw := tar.NewWriter(&tarball)
	for _, hdr := range []*tar.Header{
		{Typeflag: tar.TypeDir, Name: "etc/", Mode: 0o755},
		{Typeflag: tar.TypeReg, Name: "etc/hosts", Mode: 0o644, Size: 4},
		{Typeflag: tar.TypeReg, Name: "a.txt", Mode: 0o644, Size: 3},
		{Typeflag: tar.TypeLink, Name: "b.txt", Linkname: "a.txt"},
		{Typeflag: tar.TypeSymlink, Name: "link", Linkname: "a.txt"},
	} {
		require.NoError(t, tw.WriteHeader(hdr))
		_, err := tw.Write(bytes.Repeat([]byte("x"), int(hdr.Size)))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())

	fsys, err := TarballFS(bytes.NewReader(tarball.Bytes()))
	require.NoError(t, err)

	headers := readCPIO(t, createCPIO(t, fsys))

	var names []string
	var inodes []int64
	for _, hdr := range headers {
		names = append(names, hdr.Name)
		inodes = append(inodes, hdr.Inode)
	}
	assert.Equal(t, []string{"./a.txt", "./b.txt", "./etc", "./etc/hosts", "./link"}, names)
	assert.Equal(t, []int64{1, 1, 2, 3, 4}, inodes, "links to one file must share one inode")
	assert.Equal(t, int64(3), headers[0].Size)
	assert.Zero(t, headers[1].Size, "only the first link carries the data")
}

// newTree writes a tree with a directory, files and a hardlink. Each call
// gives the same tree in a new location.
func newTree(t *testing.T) string {
	t.Helper()

	mtime := time.Unix(1700000000, 0)
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "etc"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "etc", "hosts"), []byte("127.0.0.1 localhost\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("aaa"), 0o644))
	require.NoError(t, os.Link(filepath.Join(root, "a.txt"), filepath.Join(root, "b.txt")))
	for _, p := range []string{"etc/hosts", "a.txt", "etc", "."} {
		require.NoError(t, os.Chtimes(filepath.Join(root, p), mtime, mtime))
	}
	return root
}

// TestCreateCPIOStreamsFiles asserts that a file goes into the archive in
// pieces, so that a large file of a rootfs is never whole in memory.
func TestCreateCPIOStreamsFiles(t *testing.T) {
	const size = 64 << 20
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "big"))
	require.NoError(t, err)
	require.NoError(t, f.Truncate(size))
	require.NoError(t, f.Close())

	var archive countingWriter
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	require.NoError(t, CreateCPIO(t.Context(), &archive, os.DirFS(dir)))
	runtime.ReadMemStats(&after)

	assert.Greater(t, int64(archive), int64(size))
	assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(size/4))
}

// TestCreateCPIORefusesFilesOf4GiB asserts that a file that the size field
// of a newc header cannot hold is refused, rather than packed with a cut size.
func TestCreateCPIORefusesFilesOf4GiB(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "huge"))
	require.NoError(t, err)
	require.NoError(t, f.Truncate(1<<32))
	require.NoError(t, f.Close())

	var archive countingWriter
	err = CreateCPIO(t.Context(), &archive, os.DirFS(dir))
	require.ErrorContains(t, err, "more than CPIO holds")
	assert.Less(t, int64(archive), int64(1<<20))
}

// countingWriter counts the bytes written to it, and keeps none.
type countingWriter int64

func (w *countingWriter) Write(p []byte) (int, error) {
	*w += countingWriter(len(p))
	return len(p), nil
}

func TestCreateCPIOKeepsSpecialBits(t *testing.T) {
	headers := readCPIO(t, createCPIO(t, fstest.MapFS{
		"tmp":      {Mode: fs.ModeDir | fs.ModeSticky | 0o777},
		"mail":     {Mode: fs.ModeDir | fs.ModeSetgid | 0o775},
		"bin/sudo": {Data: []byte("sudo"), Mode: fs.ModeSetuid | 0o755},
	}))

	tmp := headerByName(t, headers, "./tmp")
	assert.Equal(t, cpio.FileMode(cpio.ModeSticky|0o777), tmp.Mode&^cpio.ModeType)
	assert.True(t, tmp.Mode.IsDir(), "the type bits must stay")
	mail := headerByName(t, headers, "./mail")
	assert.Equal(t, cpio.FileMode(cpio.ModeSetgid|0o775), mail.Mode&^cpio.ModeType)
	sudo := headerByName(t, headers, "./bin/sudo")
	assert.Equal(t, cpio.FileMode(cpio.ModeSetuid|0o755), sudo.Mode&^cpio.ModeType)
	assert.Equal(t, cpio.FileMode(cpio.TypeRegular), sudo.Mode&cpio.ModeType, "the type bits must stay")
}

func TestCreateCPIOIdenticalTreesAreIdentical(t *testing.T) {
	first := createCPIO(t, os.DirFS(newTree(t)))
	second := createCPIO(t, os.DirFS(newTree(t)))
	require.Equal(t, first, second, "identical trees in different locations must give identical archives")
}

func TestCreateCPIOFileInfoMap(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "etc"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "etc", "shadow"), []byte("root:!\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("aaa"), 0o644))

	fileInfo := WithFileInfoMap(map[string]erofs.FileInfo{
		"/etc":        {Uid: 0, Gid: 0, Mode: 0o750},
		"/etc/shadow": {Uid: 12, Gid: 34, Mode: 0o600},
	})

	t.Run("source owners", func(t *testing.T) {
		headers := readCPIO(t, createCPIO(t, os.DirFS(root), fileInfo))

		shadow := headerByName(t, headers, "./etc/shadow")
		assert.Equal(t, 12, shadow.Uid)
		assert.Equal(t, 34, shadow.Guid)
		assert.Equal(t, cpio.FileMode(0o600), shadow.Mode.Perm())
		assert.True(t, shadow.Mode.IsRegular(), "the type bits must stay")

		dir := headerByName(t, headers, "./etc")
		assert.Equal(t, cpio.FileMode(0o750), dir.Mode.Perm())
		assert.True(t, dir.Mode.IsDir(), "the type bits must stay")

		source, err := os.Stat(filepath.Join(root, "a.txt"))
		require.NoError(t, err)
		unlisted := headerByName(t, headers, "./a.txt")
		assert.Equal(t, cpio.FileMode(source.Mode().Perm()), unlisted.Mode.Perm())
		assert.Equal(t, archivefs.GetUID(source.Sys()), unlisted.Uid)
	})

	t.Run("all root", func(t *testing.T) {
		headers := readCPIO(t, createCPIO(t, os.DirFS(root), fileInfo, WithAllRoot(true)))

		shadow := headerByName(t, headers, "./etc/shadow")
		assert.Zero(t, shadow.Uid)
		assert.Zero(t, shadow.Guid)
	})
}
