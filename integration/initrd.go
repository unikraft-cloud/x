// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package integration

import (
	"io"
	"io/fs"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	archivefs "github.com/unikraft/go-archivefs"
	"github.com/unikraft/go-archivefs/cpio"
	goerofs "github.com/unikraft/go-archivefs/erofs"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/kraftfile"
)

// OpenErofsInitrdImage opens the initrd from img as an erofs.Image.
func OpenErofsInitrdImage(t testing.TB, img *imagespec.Image) *goerofs.Image {
	t.Helper()
	require.NotNil(t, img.Initrd)

	ctx := t.Context()
	rc, _, err := img.Initrd.Open(ctx)
	require.NoError(t, err)
	defer rc.Close()

	tmp, err := os.CreateTemp(t.TempDir(), "erofs-*.img")
	require.NoError(t, err)
	t.Cleanup(func() { tmp.Close() })

	_, err = io.Copy(tmp, rc)
	require.NoError(t, err)

	erofsImg, err := goerofs.OpenImage(tmp)
	require.NoError(t, err)

	return erofsImg
}

// WalkErofsInodes walks inodes in erofsImg and returns a path -> Inode map.
func WalkErofsInodes(t testing.TB, erofsImg *goerofs.Image) map[string]goerofs.Inode {
	t.Helper()
	result := make(map[string]goerofs.Inode)

	var walk func(prefix string, nid uint64)
	walk = func(prefix string, nid uint64) {
		ino, err := erofsImg.Inode(nid)
		require.NoError(t, err)

		err = ino.IterDirents(func(name string, _ uint8, childNid uint64) error {
			if name == "." || name == ".." {
				return nil
			}

			path := name
			if prefix != "" {
				path = prefix + "/" + name
			}

			childIno, err := erofsImg.Inode(childNid)
			if err != nil {
				return err
			}
			result[path] = childIno
			if childIno.IsDir() {
				walk(path, childNid)
			}
			return nil
		})
		require.NoError(t, err)
	}

	walk("", erofsImg.RootNid())
	return result
}

// ReadCpioInitrd opens the initrd from img and returns a map of file name to
// content for all regular files in the CPIO archive.
func ReadCpioInitrd(t testing.TB, img *imagespec.Image) map[string]string {
	t.Helper()
	return readFiles(t, OpenInitrd(t, img.Initrd, kraftfile.FsTypeCpio))
}

// ReadErofsInitrd opens the initrd from img and returns a map of file name to
// content for all regular files in the EROFS archive.
func ReadErofsInitrd(t testing.TB, img *imagespec.Image) map[string]string {
	t.Helper()
	return readFiles(t, OpenInitrd(t, img.Initrd, kraftfile.FsTypeErofs))
}

// OpenInitrd opens the filesystem image f in format as an fs.FS.
func OpenInitrd(t testing.TB, f imagespec.File, format kraftfile.FsType) fs.FS {
	t.Helper()
	require.NotNil(t, f)

	rc, _, err := f.Open(t.Context())
	require.NoError(t, err)
	defer rc.Close()

	// Both readers need io.ReaderAt; copy the stream into a temporary file.
	tmp, err := os.CreateTemp(t.TempDir(), "initrd-*.img")
	require.NoError(t, err)
	t.Cleanup(func() { tmp.Close() })
	_, err = io.Copy(tmp, rc)
	require.NoError(t, err)

	switch format {
	case kraftfile.FsTypeCpio:
		fsys, err := cpio.OpenFS(tmp)
		require.NoError(t, err)
		return fsys
	case kraftfile.FsTypeErofs:
		fsys, err := goerofs.Open(tmp)
		require.NoError(t, err)
		return fsys
	}
	t.Fatalf("unknown format %q", format)
	return nil
}

// readFiles returns a map of file name to content for all regular files in fsys.
func readFiles(t testing.TB, fsys fs.FS) map[string]string {
	t.Helper()

	files := make(map[string]string)
	require.NoError(t, fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		data, err := fs.ReadFile(fsys, path)
		files[path] = string(data)
		return err
	}))
	return files
}

// Owner is the uid and gid of an entry in a built root filesystem.
type Owner struct{ UID, GID int }

// ReadOwners returns the owner of every entry in the filesystem image f.
func ReadOwners(t testing.TB, f imagespec.File, format kraftfile.FsType) map[string]Owner {
	t.Helper()

	owners := make(map[string]Owner)
	fsys := OpenInitrd(t, f, format)
	require.NoError(t, fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		owners[path] = Owner{archivefs.GetUID(info.Sys()), archivefs.GetGID(info.Sys())}
		return nil
	}))
	return owners
}
