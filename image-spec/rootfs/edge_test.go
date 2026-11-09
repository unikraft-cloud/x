// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package rootfs

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFSInterface(t *testing.T) {
	t.Parallel()

	for _, opts := range [][]Option{{InMemory()}, {WithSpoolDir(t.TempDir())}} {
		layers, _ := layersOf(t, imageLayers())
		f, err := Merge(context.Background(), layers, opts...)
		require.NoError(t, err)
		require.NoError(t, fstest.TestFS(f, "usr/bin/ping", "usr/bin/ping6", "etc/new", "home/u/f", "dev/null", "bin", "big"))
		require.NoError(t, f.Close())
	}
}

func TestRootHeader(t *testing.T) {
	t.Parallel()

	root := dir(".")
	root.h.Name, root.h.Mode, root.h.Uid = "./", 0o700, 7
	f, _ := compare(t, [][]ent{{root, reg("a", "1")}})
	fi, err := f.Stat(".")
	require.NoError(t, err)
	require.Equal(t, fs.ModeDir|0o700, fi.Mode())
	require.Equal(t, uint32(7), fi.Sys().(*Inode).UID)
}

func TestIgnoredEntries(t *testing.T) {
	t.Parallel()

	global := ent{h: &tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "global", PAXRecords: map[string]string{"comment": "x"}}}
	layers, _ := layersOf(t, [][]ent{{global, reg("a", "1"), reg(".wh.", ""), reg("d/.wh.", "")}})
	f, err := Merge(context.Background(), layers, InMemory())
	require.NoError(t, err)
	require.NoError(t, fstest.TestFS(f, "a"))
	_, err = f.Lstat("d")
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestDanglingLinks(t *testing.T) {
	t.Parallel()

	// Like a link to a removed file, links without a valid target are not in the result.
	layers, _ := layersOf(t, [][]ent{
		{reg("keep", "k"), dir("d")},
		{link("empty", ""), link("missing", "nothere"), link("todir", "d"), link("lower", "gone"), reg("ok", "1")},
	})
	f, err := Merge(context.Background(), layers, InMemory())
	require.NoError(t, err)
	require.NoError(t, fstest.TestFS(f, "keep", "d", "ok"))
	for _, name := range []string{"empty", "missing", "todir", "lower"} {
		_, err := f.Lstat(name)
		require.ErrorIs(t, err, fs.ErrNotExist, name)
	}
}

func TestSpoolFileAlign(t *testing.T) {
	t.Parallel()

	spool, err := os.Create(filepath.Join(t.TempDir(), "spool"))
	require.NoError(t, err)
	defer func() { require.NoError(t, spool.Close()) }()
	layers, _ := layersOf(t, [][]ent{{reg("a", "123"), reg("b", "45678"), reg("empty", "")}})
	f, err := Merge(context.Background(), layers, WithSpoolFile(spool), WithAlign(4096))
	require.NoError(t, err)
	// Close does not close a spool file of the caller.
	require.NoError(t, f.Close())

	for name, data := range map[string]string{"a": "123", "b": "45678"} {
		off, ok := f.DataOffset(name)
		require.True(t, ok, name)
		require.Zero(t, off%4096, name)
		got := make([]byte, len(data))
		_, err := spool.ReadAt(got, off)
		require.NoError(t, err)
		require.Equal(t, data, string(got))
	}
	_, ok := f.DataOffset(".")
	require.False(t, ok)
}

func TestMergeErrors(t *testing.T) {
	t.Parallel()

	good := tarOf(t, []ent{reg("a", string(bytes.Repeat([]byte{'x'}, 2000)))})
	errOpen := errors.New("open failed")
	for name, layer := range map[string]Layer{
		"open": func() (io.ReadCloser, error) { return nil, errOpen },
		"corrupt": func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bytes.Repeat([]byte{1}, 1024))), nil
		},
		"short": func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(good[:1500])), nil },
	} {
		_, err := Merge(context.Background(), []Layer{layer}, WithSpoolDir(t.TempDir()))
		require.Error(t, err, name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	_, err := Merge(ctx, []Layer{func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(good)), nil }})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestLinkTargetHiddenBetween(t *testing.T) {
	t.Parallel()

	// A layer between the link and its target hides the target, so the link has no target.
	for name, hide := range map[string][]ent{
		"opaque":             {opq("d")},
		"whiteout":           {wh("d")},
		"file":               {reg("d", "file")},
		"opaque, new target": {opq("d"), reg("d/x", "new")},
		"file after target":  {reg("d/x", "mid"), reg("d", "file")},
	} {
		layers, _ := layersOf(t, [][]ent{{reg("d/x", "stale")}, hide, {link("a", "d/x")}})
		f, err := Merge(context.Background(), layers, InMemory())
		require.NoError(t, err, name)
		got, err := fs.ReadFile(f, "a")
		if name == "opaque, new target" {
			require.NoError(t, err, name)
			require.Equal(t, "new", string(got), name)
			continue
		}
		require.ErrorIs(t, err, fs.ErrNotExist, name)
	}
}

func TestLinkWithoutTargetHides(t *testing.T) {
	t.Parallel()

	layers, _ := layersOf(t, [][]ent{{reg("bin/sh", "lower")}, {reg("bin/sh", "mid"), dir("d"), link("bin/sh", "d")}})
	f, err := Merge(context.Background(), layers, InMemory())
	require.NoError(t, err)
	_, err = f.Lstat("bin/sh")
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestInMemoryHugeHeader(t *testing.T) {
	t.Parallel()

	// The header claims more data than the layer has.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "big", Mode: 0o644, Size: 1 << 40, Format: tar.FormatPAX}))
	_, err := tw.Write([]byte("only this"))
	require.NoError(t, err)
	layer := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(buf.Bytes())), nil }
	_, err = Merge(context.Background(), []Layer{layer}, InMemory())
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestSpoolFileOffsetZero(t *testing.T) {
	t.Parallel()

	spool, err := os.Create(filepath.Join(t.TempDir(), "spool"))
	require.NoError(t, err)
	defer func() { require.NoError(t, spool.Close()) }()
	_, err = spool.WriteString("old data of the caller")
	require.NoError(t, err)
	layers, _ := layersOf(t, [][]ent{{reg("a", "hello")}})
	f, err := Merge(context.Background(), layers, WithSpoolFile(spool))
	require.NoError(t, err)
	off, ok := f.DataOffset("a")
	require.True(t, ok)
	require.Zero(t, off)
	got, err := fs.ReadFile(f, "a")
	require.NoError(t, err)
	require.Equal(t, "hello", string(got))
}

func TestAUFSMetadata(t *testing.T) {
	t.Parallel()

	// Like in docker, a hard link to a file in .wh..wh.plnk still works.
	layers, _ := layersOf(t, [][]ent{{
		dir(".wh..wh.plnk"), reg(".wh..wh.plnk/123.456", "x"), reg(".wh..wh.aufs", ""), dir(".wh..wh.orph"), reg("a", "1"),
		link("b", ".wh..wh.plnk/123.456"),
	}})
	f, err := Merge(context.Background(), layers, InMemory())
	require.NoError(t, err)
	require.NoError(t, fstest.TestFS(f, "a", "b"))
	entries, err := f.ReadDir(".")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	got, err := fs.ReadFile(f, "b")
	require.NoError(t, err)
	require.Equal(t, "x", string(got))
	fi, err := f.Lstat("b")
	require.NoError(t, err)
	require.Equal(t, uint32(1), fi.Sys().(*Inode).Nlink)
}

func TestSymlinkLookup(t *testing.T) {
	t.Parallel()

	layers, _ := layersOf(t, [][]ent{{reg("f", "1"), sym("empty", ""), sym("up", "f/.."), sym("slash", "f/")}})
	f, err := Merge(context.Background(), layers, InMemory())
	require.NoError(t, err)
	for _, name := range []string{"empty", "up", "slash"} {
		_, err := f.Stat(name)
		require.ErrorIs(t, err, fs.ErrNotExist, name)
		_, err = f.Lstat(name)
		require.NoError(t, err, name)
	}
}

func TestStatSymlinkName(t *testing.T) {
	t.Parallel()

	layers, _ := layersOf(t, [][]ent{{reg("real", "1"), sym("ln", "real")}})
	f, err := Merge(context.Background(), layers, InMemory())
	require.NoError(t, err)
	fi, err := f.Stat("ln")
	require.NoError(t, err)
	require.Equal(t, "ln", fi.Name())
	require.Equal(t, int64(1), fi.Size())
	h, err := f.Open("ln")
	require.NoError(t, err)
	fi, err = h.Stat()
	require.NoError(t, err)
	require.Equal(t, "ln", fi.Name())
	require.NoError(t, h.Close())
}
