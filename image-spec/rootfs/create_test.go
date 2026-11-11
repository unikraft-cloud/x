// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package rootfs

import (
	"archive/tar"
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unikraft/go-archivefs"
	"github.com/unikraft/go-archivefs/cpio"
	"github.com/unikraft/go-archivefs/erofs"
)

// imageLayers is a small image with the types and the metadata that an image keeps.
func imageLayers() [][]ent {
	ping := reg("usr/bin/ping", "elf")
	ping.h.Mode = 0o4755
	ping.h.PAXRecords = map[string]string{
		"SCHILY.xattr.security.capability": "\x01\x00\x00\x02\x00\x20\x00\x00",
		"SCHILY.xattr.user.a%3Db":          "escaped",
	}
	owned := reg("home/u/f", "data")
	owned.h.Uid, owned.h.Gid = 1000, 1001
	return [][]ent{
		{
			dir("usr"), dir("usr/bin"), ping, link("usr/bin/ping6", "usr/bin/ping"),
			dir("etc"), reg("etc/old", "old"), dir("home"), dir("home/u"), owned,
			{h: &tar.Header{Typeflag: tar.TypeChar, Name: "dev/null", Mode: 0o666, Devmajor: 1, Devminor: 3, ModTime: t0}},
			sym("bin", "usr/bin"), reg("big", strings.Repeat("b", 10000)),
		},
		{wh("etc/old"), reg("etc/new", "new")},
	}
}

// mergedImage returns the merged tree of imageLayers, to compare an image with it.
func mergedImage(t *testing.T) *FS {
	layers, _ := layersOf(t, imageLayers())
	f, err := Merge(context.Background(), layers, InMemory())
	require.NoError(t, err)
	return f
}

// sameTree checks that got has the entries of want, with the same metadata, data and hard links.
func sameTree(t *testing.T, want, got fs.FS, xattrs bool) {
	t.Helper()
	inos := map[uint64]uint64{}
	n := 0
	require.NoError(t, fs.WalkDir(want, ".", func(name string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		n++
		w, err := d.Info()
		require.NoError(t, err)
		g, err := fs.Lstat(got, name)
		require.NoError(t, err, name)
		if name == "." {
			return nil
		}
		require.Equal(t, w.Mode(), g.Mode(), name)
		require.Equal(t, archivefs.GetUID(w.Sys()), archivefs.GetUID(g.Sys()), name)
		require.Equal(t, archivefs.GetGID(w.Sys()), archivefs.GetGID(g.Sys()), name)
		switch w.Mode().Type() {
		case 0:
			wd, err := fs.ReadFile(want, name)
			require.NoError(t, err)
			gd, err := fs.ReadFile(got, name)
			require.NoError(t, err, name)
			require.Equal(t, wd, gd, name)
		case fs.ModeSymlink:
			wt, err := fs.ReadLink(want, name)
			require.NoError(t, err)
			gt, err := fs.ReadLink(got, name)
			require.NoError(t, err, name)
			require.Equal(t, wt, gt, name)
		case fs.ModeDevice | fs.ModeCharDevice, fs.ModeDevice:
			require.Equal(t, archivefs.GetDevMajor(w.Sys()), archivefs.GetDevMajor(g.Sys()), name)
			require.Equal(t, archivefs.GetDevMinor(w.Sys()), archivefs.GetDevMinor(g.Sys()), name)
		}
		if !d.IsDir() {
			wi, gi := archivefs.GetIno(w.Sys()), archivefs.GetIno(g.Sys())
			if prev, ok := inos[wi]; ok {
				require.Equal(t, prev, gi, "%s must be a hard link", name)
			}
			inos[wi] = gi
		}
		if xattrs {
			wx, err := archivefs.GetXattrs(w.Sys())
			require.NoError(t, err)
			gx, err := archivefs.GetXattrs(g.Sys())
			require.NoError(t, err)
			require.Len(t, gx, len(wx), name)
			for k, v := range wx {
				require.Equal(t, v, gx[k], "%s: %s", name, k)
			}
		}
		return nil
	}))
	m := 0
	require.NoError(t, fs.WalkDir(got, ".", func(string, fs.DirEntry, error) error { m++; return nil }))
	require.Equal(t, n, m, "the number of entries")
}

func TestEROFS(t *testing.T) {
	t.Parallel()

	write := func() []byte {
		layers, _ := layersOf(t, imageLayers())
		f, err := os.Create(filepath.Join(t.TempDir(), "img"))
		require.NoError(t, err)
		defer func() { require.NoError(t, f.Close()) }()
		require.NoError(t, EROFS(context.Background(), f, layers, WithSpoolDir(t.TempDir())))
		if fsck, err := exec.LookPath("fsck.erofs"); err == nil {
			out, err := exec.Command(fsck, f.Name()).CombinedOutput()
			require.NoError(t, err, string(out))
		}
		img, err := os.ReadFile(f.Name())
		require.NoError(t, err)
		return img
	}
	img := write()
	require.True(t, bytes.Equal(img, write()), "two images of the same layers must be the same")

	got, err := erofs.Open(bytes.NewReader(img))
	require.NoError(t, err)
	sameTree(t, mergedImage(t), got, true)

	fi, err := got.Lstat("usr/bin/ping")
	require.NoError(t, err)
	xattrs, err := archivefs.GetXattrs(fi.Sys())
	require.NoError(t, err)
	require.Equal(t, "escaped", xattrs["user.a=b"])
}

func TestCPIO(t *testing.T) {
	t.Parallel()

	write := func() []byte {
		layers, _ := layersOf(t, imageLayers())
		var buf bytes.Buffer
		require.NoError(t, CPIO(context.Background(), &buf, layers, InMemory()))
		return buf.Bytes()
	}
	archive := write()
	require.Equal(t, archive, write(), "two archives of the same layers must be the same")

	got, err := cpio.OpenFS(bytes.NewReader(archive))
	require.NoError(t, err)
	sameTree(t, mergedImage(t), got, false)
}

func TestCreateCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	layers, _ := layersOf(t, imageLayers())
	f, err := os.Create(filepath.Join(t.TempDir(), "img"))
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	require.ErrorIs(t, EROFS(ctx, f, layers, InMemory()), context.Canceled)
	require.ErrorIs(t, CPIO(ctx, &bytes.Buffer{}, layers, InMemory()), context.Canceled)
}
