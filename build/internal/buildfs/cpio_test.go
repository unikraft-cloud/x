// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package buildfs

import (
	"archive/tar"
	"bytes"
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
	"github.com/unikraft/go-archivefs/cpio"
	"github.com/unikraft/go-archivefs/erofs"
)

// openCPIO opens a CPIO archive as an fs.FS.
func openCPIO(t *testing.T, data []byte) *cpio.FS {
	t.Helper()

	fsys, err := cpio.OpenFS(bytes.NewReader(data))
	require.NoError(t, err)
	return fsys
}

// lstat returns the file information of name in fsys.
func lstat(t *testing.T, fsys fs.FS, name string) fs.FileInfo {
	t.Helper()

	info, err := fs.Lstat(fsys, name)
	require.NoError(t, err)
	return info
}

func createCPIO(t *testing.T, fsys fs.FS, opts ...CreateOption) []byte {
	t.Helper()

	var buf bytes.Buffer
	require.NoError(t, CreateCPIO(&buf, fsys, opts...))
	return buf.Bytes()
}

func TestCreateCPIOTarballHardlinks(t *testing.T) {
	var tarball bytes.Buffer
	tw := tar.NewWriter(&tarball)
	for _, hdr := range []*tar.Header{
		{Typeflag: tar.TypeReg, Name: "a.txt", Mode: 0o644, Size: 3},
		{Typeflag: tar.TypeLink, Name: "b.txt", Linkname: "a.txt"},
	} {
		require.NoError(t, tw.WriteHeader(hdr))
		_, err := tw.Write(bytes.Repeat([]byte("x"), int(hdr.Size)))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())

	fsys, err := TarballFS(bytes.NewReader(tarball.Bytes()))
	require.NoError(t, err)

	archive := openCPIO(t, createCPIO(t, fsys))

	a, b := lstat(t, archive, "a.txt"), lstat(t, archive, "b.txt")
	assert.Equal(t, archivefs.GetIno(a.Sys()), archivefs.GetIno(b.Sys()), "links to one file must share one inode")
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
	require.NoError(t, CreateCPIO(&archive, os.DirFS(dir)))
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
	err = CreateCPIO(&archive, os.DirFS(dir))
	require.ErrorIs(t, err, cpio.ErrHeader)
	assert.Less(t, int64(archive), int64(1<<20))
}

// countingWriter counts the bytes written to it, and keeps none.
type countingWriter int64

func (w *countingWriter) Write(p []byte) (int, error) {
	*w += countingWriter(len(p))
	return len(p), nil
}

func TestCreateCPIOKeepsSpecialBits(t *testing.T) {
	fsys := openCPIO(t, createCPIO(t, fstest.MapFS{
		"tmp":      {Mode: fs.ModeDir | fs.ModeSticky | 0o777},
		"mail":     {Mode: fs.ModeDir | fs.ModeSetgid | 0o775},
		"bin/sudo": {Data: []byte("sudo"), Mode: fs.ModeSetuid | 0o755},
	}))

	assert.Equal(t, fs.ModeDir|fs.ModeSticky|0o777, lstat(t, fsys, "tmp").Mode())
	assert.Equal(t, fs.ModeDir|fs.ModeSetgid|0o775, lstat(t, fsys, "mail").Mode())
	assert.Equal(t, fs.ModeSetuid|0o755, lstat(t, fsys, "bin/sudo").Mode())
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
		fsys := openCPIO(t, createCPIO(t, os.DirFS(root), fileInfo))

		shadow := lstat(t, fsys, "etc/shadow")
		assert.Equal(t, 12, archivefs.GetUID(shadow.Sys()))
		assert.Equal(t, 34, archivefs.GetGID(shadow.Sys()))
		assert.Equal(t, fs.FileMode(0o600), shadow.Mode(), "the type bits must stay")

		dir := lstat(t, fsys, "etc")
		assert.Equal(t, fs.ModeDir|0o750, dir.Mode(), "the type bits must stay")

		source, err := os.Stat(filepath.Join(root, "a.txt"))
		require.NoError(t, err)
		unlisted := lstat(t, fsys, "a.txt")
		assert.Equal(t, source.Mode().Perm(), unlisted.Mode().Perm())
		assert.Equal(t, archivefs.GetUID(source.Sys()), archivefs.GetUID(unlisted.Sys()))
	})

	t.Run("all root", func(t *testing.T) {
		fsys := openCPIO(t, createCPIO(t, os.DirFS(root), fileInfo, WithAllRoot(true)))

		shadow := lstat(t, fsys, "etc/shadow")
		assert.Zero(t, archivefs.GetUID(shadow.Sys()))
		assert.Zero(t, archivefs.GetGID(shadow.Sys()))
	})
}
