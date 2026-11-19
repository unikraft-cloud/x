// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package mergefs

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFSInterface(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		opts []Option
	}{
		{name: "in memory", opts: []Option{InMemory()}},
		{name: "spool", opts: []Option{WithSpoolDir(t.TempDir())}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			layers, _ := tarLayers(t, imageLayers())
			f, err := Merge(context.Background(), layers, tt.opts...)
			require.NoError(t, err)
			require.NoError(t, fstest.TestFS(f, "usr/bin/ping", "usr/bin/ping6", "etc/new", "home/u/f", "dev/null", "bin", "big"))
			require.NoError(t, f.Close())
		})
	}
}

func TestRootHeader(t *testing.T) {
	t.Parallel()

	root := dir(".")
	root.h.Name, root.h.Mode, root.h.Uid = "./", 0o700, 7
	f := compare(t, [][]ent{{root, reg("a", "1")}})
	fi, err := f.Stat(".")
	require.NoError(t, err)
	require.Equal(t, fs.ModeDir|0o700, fi.Mode())
	require.Equal(t, uint32(7), fi.Sys().(*Inode).UID)
}

func TestIgnoredEntries(t *testing.T) {
	t.Parallel()

	global := ent{h: &tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "global", PAXRecords: map[string]string{"comment": "x"}}}
	layers, _ := tarLayers(t, [][]ent{{global, reg("a", "1"), reg(".wh.", ""), reg("d/.wh.", "")}})
	f, err := Merge(context.Background(), layers, InMemory())
	require.NoError(t, err)
	require.NoError(t, fstest.TestFS(f, "a"))
	_, err = f.Lstat("d")
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestDanglingLinks(t *testing.T) {
	t.Parallel()

	// Like a link to a removed file, links without a valid target are not in the result.
	layers, _ := tarLayers(t, [][]ent{
		{reg("keep", "k"), dir("d")},
		{link("empty", ""), link("missing", "nothere"), link("todir", "d"), link("lower", "gone"), reg("ok", "1")},
	})
	f, err := Merge(context.Background(), layers, InMemory())
	require.NoError(t, err)
	require.NoError(t, fstest.TestFS(f, "keep", "d", "ok"))
	for _, name := range []string{"empty", "missing", "todir", "lower"} {
		t.Run(name, func(t *testing.T) {
			_, err := f.Lstat(name)
			require.ErrorIs(t, err, fs.ErrNotExist)
		})
	}
}

// withSpoolFile makes Merge write the file data to f, so that a test can check the spool.
func withSpoolFile(f *os.File) Option { return func(o *options) { o.spoolFile = f } }

func TestSpoolHasOnlyVisibleData(t *testing.T) {
	t.Parallel()

	spool, err := os.Create(filepath.Join(t.TempDir(), "spool"))
	require.NoError(t, err)
	defer func() { require.NoError(t, spool.Close()) }()
	big := strings.Repeat("x", 1<<20)
	layers, opens := tarLayers(t, [][]ent{
		{reg("a", big), reg("b", big), link("c", "b")},
		{reg("a", "new"), reg("b", "new")},
	})
	f, err := Merge(context.Background(), layers, withSpoolFile(spool))
	require.NoError(t, err)
	got, err := fs.ReadFile(f, "c")
	require.NoError(t, err)
	require.Equal(t, big, string(got))
	// Close does not close a spool file of the caller.
	require.NoError(t, f.Close())
	fi, err := spool.Stat()
	require.NoError(t, err)
	// The hidden a is not in the spool, and the hidden b is in it because the link c needs it.
	require.Equal(t, int64(len(big)+2*len("new")), fi.Size())
	require.Equal(t, 2, *opens)
}

func TestMergeErrors(t *testing.T) {
	t.Parallel()

	good := tarOf(t, []ent{reg("a", string(bytes.Repeat([]byte{'x'}, 2000)))})
	errOpen := errors.New("open failed")
	for _, tt := range []struct {
		name  string
		layer Layer
	}{
		{name: "open", layer: func() (io.ReadCloser, error) { return nil, errOpen }},
		{name: "corrupt", layer: func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bytes.Repeat([]byte{1}, 1024))), nil
		}},
		{name: "short", layer: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(good[:1500])), nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Merge(context.Background(), []Layer{tt.layer}, WithSpoolDir(t.TempDir()))
			require.Error(t, err)
		})
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
	for _, tt := range []struct {
		name string
		hide []ent
		want string // the data of the link, or "" when it has no target
	}{
		{name: "opaque", hide: []ent{opq("d")}},
		{name: "whiteout", hide: []ent{wh("d")}},
		{name: "file", hide: []ent{reg("d", "file")}},
		{name: "opaque, new target", hide: []ent{opq("d"), reg("d/x", "new")}, want: "new"},
		{name: "file after target", hide: []ent{reg("d/x", "mid"), reg("d", "file")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			layers, _ := tarLayers(t, [][]ent{{reg("d/x", "stale")}, tt.hide, {link("a", "d/x")}})
			f, err := Merge(context.Background(), layers, InMemory())
			require.NoError(t, err)
			got, err := fs.ReadFile(f, "a")
			if tt.want == "" {
				require.ErrorIs(t, err, fs.ErrNotExist)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, string(got))
		})
	}
}

func TestLinkWithoutTargetHides(t *testing.T) {
	t.Parallel()

	layers, _ := tarLayers(t, [][]ent{{reg("bin/sh", "lower")}, {reg("bin/sh", "mid"), dir("d"), link("bin/sh", "d")}})
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

func TestAUFSMetadata(t *testing.T) {
	t.Parallel()

	// Like in docker, a hard link to a file in .wh..wh.plnk still works.
	layers, _ := tarLayers(t, [][]ent{{
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

	layers, _ := tarLayers(t, [][]ent{{reg("f", "1"), sym("empty", ""), sym("up", "f/.."), sym("slash", "f/")}})
	f, err := Merge(context.Background(), layers, InMemory())
	require.NoError(t, err)
	for _, name := range []string{"empty", "up", "slash"} {
		t.Run(name, func(t *testing.T) {
			_, err := f.Stat(name)
			require.ErrorIs(t, err, fs.ErrNotExist)
			_, err = f.Lstat(name)
			require.NoError(t, err)
		})
	}
}

func TestStatSymlinkName(t *testing.T) {
	t.Parallel()

	layers, _ := tarLayers(t, [][]ent{{reg("real", "1"), sym("ln", "real")}})
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

func TestSpoolRemoved(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	layers, _ := tarLayers(t, [][]ent{{reg("a", "1")}})
	f, err := Merge(context.Background(), layers, WithSpoolDir(dir))
	require.NoError(t, err)
	require.NoError(t, f.Close())
	left, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, left)
}

func TestCancelInFile(t *testing.T) {
	t.Parallel()

	// The context ends while the data of a large file is read.
	for _, tt := range []struct {
		name string
		opts []Option
	}{
		{name: "in memory", opts: []Option{InMemory()}},
		{name: "spool", opts: []Option{WithSpoolDir(t.TempDir())}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			layers, _ := tarLayers(t, [][]ent{{reg("big", strings.Repeat("b", 4<<20))}})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var ca *cancelAfter
			layer := func() (io.ReadCloser, error) {
				rc, err := layers[0]()
				ca = &cancelAfter{ReadCloser: rc, cancel: cancel, n: 1 << 20}
				return ca, err
			}
			_, err := Merge(ctx, []Layer{layer}, tt.opts...)
			require.ErrorIs(t, err, context.Canceled)
			require.Less(t, ca.read, 4<<20, "the merge read the whole file")
		})
	}
}

// cancelAfter cancels a context after n bytes of its reader.
type cancelAfter struct {
	io.ReadCloser
	cancel context.CancelFunc
	n      int
	read   int
}

func (c *cancelAfter) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.read += n
	c.n -= n
	if c.n <= 0 {
		c.cancel()
	}
	return n, err
}

func TestManyLowerLinks(t *testing.T) {
	t.Parallel()

	// Each link of the upper layer waits for a file of the lower layer.
	const n = 20000
	lower := make([]ent, 0, n)
	upper := make([]ent, 0, n)
	for i := range n {
		lower = append(lower, reg(fmt.Sprintf("f%d", i), fmt.Sprint(i)))
		upper = append(upper, link(fmt.Sprintf("l%d", i), fmt.Sprintf("f%d", i)))
	}
	layers, _ := tarLayers(t, [][]ent{lower, upper})
	f, err := Merge(context.Background(), layers, InMemory())
	require.NoError(t, err)
	for _, i := range []int{0, n / 2, n - 1} {
		got, err := fs.ReadFile(f, fmt.Sprintf("l%d", i))
		require.NoError(t, err)
		require.Equal(t, fmt.Sprint(i), string(got))
	}
}

func TestReadDirectory(t *testing.T) {
	t.Parallel()

	layers, _ := tarLayers(t, [][]ent{{dir("d"), reg("d/a", "1")}})
	f, err := Merge(context.Background(), layers, InMemory())
	require.NoError(t, err)
	h, err := f.Open("d")
	require.NoError(t, err)
	defer func() { require.NoError(t, h.Close()) }()
	_, err = io.Copy(io.Discard, h)
	require.ErrorContains(t, err, "is a directory")
}

func TestCancelInSparseHole(t *testing.T) {
	t.Parallel()

	const size = 64 << 20
	data := sparseTar(t, "big", size)
	// tar.Reader makes the hole without a read of the layer, so the cancel comes after the map, before the hole.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	layer := func() (io.ReadCloser, error) {
		return &cancelAfter{ReadCloser: io.NopCloser(bytes.NewReader(data)), cancel: cancel, n: len(data) - 1024}, nil
	}
	spool, err := os.Create(filepath.Join(t.TempDir(), "spool"))
	require.NoError(t, err)
	defer func() { require.NoError(t, spool.Close()) }()
	_, err = Merge(ctx, []Layer{layer}, withSpoolFile(spool))
	require.ErrorIs(t, err, context.Canceled)
	fi, err := spool.Stat()
	require.NoError(t, err)
	require.Less(t, fi.Size(), int64(size), "the merge wrote the hole")
}

// sparseTar returns a layer with one PAX 1.0 sparse file whose data is all a hole.
// tar.Writer drops GNU.sparse records, so it writes the PAX header as a file and then sets its type.
func sparseTar(t *testing.T, name string, size int64) []byte {
	var recs string
	for _, kv := range [][2]string{
		{"GNU.sparse.major", "1"}, {"GNU.sparse.minor", "0"}, {"GNU.sparse.name", name}, {"GNU.sparse.realsize", fmt.Sprint(size)},
	} {
		r := " " + kv[0] + "=" + kv[1] + "\n"
		n := len(r) + 1
		for len(fmt.Sprint(n))+len(r) != n {
			n++
		}
		recs += fmt.Sprint(n) + r
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "PaxHeaders.0/" + name, Mode: 0o644, Size: int64(len(recs)), Format: tar.FormatUSTAR}))
	_, err := tw.Write([]byte(recs))
	require.NoError(t, err)
	// The sparse map: no data blocks.
	block := make([]byte, 512)
	copy(block, "0\n")
	require.NoError(t, tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "GNUSparseFile.0/" + name, Mode: 0o644, Size: int64(len(block)), Format: tar.FormatUSTAR}))
	_, err = tw.Write(block)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	b := buf.Bytes()
	b[156] = tar.TypeXHeader
	copy(b[148:156], "        ")
	sum := 0
	for _, c := range b[:512] {
		sum += int(c)
	}
	copy(b[148:156], fmt.Sprintf("%06o\x00 ", sum))
	return b
}
