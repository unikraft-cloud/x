// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package integration

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	archivefs "github.com/unikraft/go-archivefs"
	goerofs "github.com/unikraft/go-archivefs/erofs"
	"github.com/unikraft/go-cpio"
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
	require.NotNil(t, img.Initrd)

	ctx := t.Context()
	f, _, err := img.Initrd.Open(ctx)
	require.NoError(t, err)
	defer f.Close()

	cr := cpio.NewReader(f)
	files := make(map[string]string)
	for {
		hdr, err := cr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)

		if hdr.Mode&cpio.TypeRegular != 0 {
			data, err := io.ReadAll(cr)
			require.NoError(t, err)
			files[hdr.Name] = string(data)
		}
	}
	return files
}

// ReadErofsInitrd opens the initrd from img and returns a map of file name to
// content for all regular files in the EROFS archive.
func ReadErofsInitrd(t testing.TB, img *imagespec.Image) map[string]string {
	t.Helper()
	require.NotNil(t, img.Initrd)

	ctx := t.Context()
	rc, _, err := img.Initrd.Open(ctx)
	require.NoError(t, err)
	defer rc.Close()

	// EROFS needs io.ReaderAt; copy the stream into a temporary file.
	tmp, err := os.CreateTemp(t.TempDir(), "erofs-*.img")
	require.NoError(t, err)
	defer tmp.Close()

	_, err = io.Copy(tmp, rc)
	require.NoError(t, err)

	fsys, err := goerofs.Open(tmp)
	require.NoError(t, err)

	files := make(map[string]string)
	readErofsDir(t, fsys, ".", files)
	return files
}

// readErofsDir recursively reads all regular files from an EROFS filesystem.
func readErofsDir(t testing.TB, fsys *goerofs.Filesystem, dir string, files map[string]string) {
	t.Helper()
	entries, err := fsys.ReadDir(dir)
	require.NoError(t, err)

	for _, entry := range entries {
		name := dir + "/" + entry.Name()
		if dir == "." {
			name = entry.Name()
		}
		if entry.IsDir() {
			readErofsDir(t, fsys, name, files)
			continue
		}
		if !entry.Type().IsRegular() {
			continue
		}
		f, err := fsys.Open(name)
		require.NoError(t, err)
		data, err := io.ReadAll(f)
		f.Close()
		require.NoError(t, err)
		files[name] = string(data)
	}
}

// Owner is the uid and gid of an entry in a built root filesystem.
type Owner struct{ UID, GID int }

// ReadOwners returns the owner of every entry in the filesystem image f.
func ReadOwners(t testing.TB, f imagespec.File, format kraftfile.FsType) map[string]Owner {
	t.Helper()
	require.NotNil(t, f)

	rc, _, err := f.Open(t.Context())
	require.NoError(t, err)
	defer rc.Close()

	owners := make(map[string]Owner)
	switch format {
	case kraftfile.FsTypeCpio:
		cr := cpio.NewReader(rc)
		for {
			hdr, err := cr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			require.NoError(t, err)
			_, err = io.Copy(io.Discard, cr)
			require.NoError(t, err)
			owners[strings.TrimPrefix(hdr.Name, "./")] = Owner{hdr.Uid, hdr.Guid}
		}
	case kraftfile.FsTypeErofs:
		// EROFS needs io.ReaderAt; copy the stream into a temporary file.
		tmp, err := os.CreateTemp(t.TempDir(), "erofs-*.img")
		require.NoError(t, err)
		defer tmp.Close()
		_, err = io.Copy(tmp, rc)
		require.NoError(t, err)

		fsys, err := goerofs.Open(tmp)
		require.NoError(t, err)
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
	default:
		t.Fatalf("unknown format %q", format)
	}
	return owners
}
