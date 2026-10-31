// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package rootfs

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	_ fs.FS         = (*FS)(nil)
	_ fs.ReadDirFS  = (*FS)(nil)
	_ fs.StatFS     = (*FS)(nil)
	_ fs.ReadLinkFS = (*FS)(nil)

	_ fs.FileInfo = (*node)(nil)
	_ fs.DirEntry = (*node)(nil)
)

func (n *node) Name() string { return n.name }

func (n *node) Size() int64 {
	switch n.ino.mode.Type() {
	case 0:
		return n.ino.size
	case fs.ModeSymlink:
		return int64(len(n.ino.target))
	}
	return 0
}

func (n *node) Mode() fs.FileMode          { return n.ino.mode }
func (n *node) ModTime() time.Time         { return n.ino.mtime }
func (n *node) IsDir() bool                { return n.kind == kDir }
func (n *node) Sys() any                   { return n.ino }
func (n *node) Type() fs.FileMode          { return n.ino.mode.Type() }
func (n *node) Info() (fs.FileInfo, error) { return n, nil }
func (n *node) String() string             { return fs.FormatFileInfo(n) }

func (n *node) child(name string) *node {
	i := sort.Search(len(n.list), func(i int) bool { return n.list[i].name >= name })
	if i < len(n.list) && n.list[i].name == name {
		return n.list[i]
	}
	return nil
}

var errLoop = errors.New("too many levels of symbolic links")

// lookup finds name. It follows symlinks in the path, and it follows a
// symlink at the end only when follow is true.
func (f *FS) lookup(op, name string, follow bool) (*node, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: op, Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		name = ""
	}
	cur, rest, hops := f.root, name, 0
	for rest != "" {
		var comp string
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			comp, rest = rest[:j], rest[j+1:]
		} else {
			comp, rest = rest, ""
		}
		switch comp {
		case "", ".":
			continue
		case "..":
			if cur.parent != nil {
				cur = cur.parent
			}
			continue
		}
		if cur.kind != kDir {
			return nil, &fs.PathError{Op: op, Path: name, Err: fs.ErrNotExist}
		}
		c := cur.child(comp)
		if c == nil {
			return nil, &fs.PathError{Op: op, Path: name, Err: fs.ErrNotExist}
		}
		if c.ino.mode.Type() == fs.ModeSymlink && (rest != "" || follow) {
			if hops++; hops > 40 {
				return nil, &fs.PathError{Op: op, Path: name, Err: errLoop}
			}
			t := c.ino.target
			if strings.HasPrefix(t, "/") {
				cur, t = f.root, strings.TrimLeft(t, "/")
			}
			if rest != "" {
				t += "/" + rest
			}
			rest = t
			continue
		}
		cur = c
	}
	return cur, nil
}

// Open opens the named file and follows symlinks.
func (f *FS) Open(name string) (fs.File, error) {
	n, err := f.lookup("open", name, true)
	if err != nil {
		return nil, err
	}
	h := &file{n: n}
	if n.kind == kOther && n.ino.mode.Type() == 0 {
		var ra io.ReaderAt
		if n.ino.data != nil {
			ra = bytes.NewReader(n.ino.data)
		} else if f.spool != nil {
			ra = f.spool
		}
		if ra != nil {
			h.sr = io.NewSectionReader(ra, n.ino.off, n.ino.size)
		}
	}
	return h, nil
}

// ReadDir returns the entries of the named directory, sorted by name.
func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	n, err := f.lookup("readdir", name, true)
	if err != nil {
		return nil, err
	}
	if n.kind != kDir {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: errors.New("not a directory")}
	}
	out := make([]fs.DirEntry, len(n.list))
	for i, c := range n.list {
		out[i] = c
	}
	return out, nil
}

// Stat returns the FileInfo of the named file and follows symlinks.
func (f *FS) Stat(name string) (fs.FileInfo, error) {
	return f.lookup("stat", name, true)
}

// Lstat returns the FileInfo of the named file. It does not follow a
// symlink at the end of the path.
func (f *FS) Lstat(name string) (fs.FileInfo, error) {
	return f.lookup("lstat", name, false)
}

// ReadLink returns the target of the named symlink.
func (f *FS) ReadLink(name string) (string, error) {
	n, err := f.lookup("readlink", name, false)
	if err != nil {
		return "", err
	}
	if n.ino.mode.Type() != fs.ModeSymlink {
		return "", &fs.PathError{Op: "readlink", Path: name, Err: fs.ErrInvalid}
	}
	return n.ino.target, nil
}

type file struct {
	n   *node
	sr  *io.SectionReader
	pos int
}

func (h *file) Stat() (fs.FileInfo, error) { return h.n, nil }
func (h *file) Close() error               { return nil }

func (h *file) Read(p []byte) (int, error) {
	if h.n.kind == kDir {
		return 0, &fs.PathError{Op: "read", Path: h.n.name, Err: errors.New("is a directory")}
	}
	if h.sr == nil {
		return 0, io.EOF
	}
	return h.sr.Read(p)
}

func (h *file) ReadAt(p []byte, off int64) (int, error) {
	if h.sr == nil {
		return 0, io.EOF
	}
	return h.sr.ReadAt(p, off)
}

func (h *file) Seek(off int64, whence int) (int64, error) {
	if h.sr == nil {
		return 0, nil
	}
	return h.sr.Seek(off, whence)
}

var bufPool = sync.Pool{New: func() any { b := make([]byte, 1<<20); return &b }}

// WriteTo copies the rest of the file with a 1 MiB buffer.
func (h *file) WriteTo(w io.Writer) (int64, error) {
	if h.sr == nil {
		return 0, nil
	}
	bp := bufPool.Get().(*[]byte)
	defer bufPool.Put(bp)
	return io.CopyBuffer(onlyWriter{w}, struct{ io.Reader }{h.sr}, *bp)
}

func (h *file) ReadDir(count int) ([]fs.DirEntry, error) {
	if h.n.kind != kDir {
		return nil, &fs.PathError{Op: "readdir", Path: h.n.name, Err: errors.New("not a directory")}
	}
	rest := h.n.list[h.pos:]
	if count > 0 && len(rest) == 0 {
		return nil, io.EOF
	}
	if count <= 0 || count > len(rest) {
		count = len(rest)
	}
	out := make([]fs.DirEntry, count)
	for i := range count {
		out[i] = rest[i]
	}
	h.pos += count
	return out, nil
}
