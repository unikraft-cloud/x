// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package rootfs merges the tar layers of an OCI image into one read-only
// fs.FS, with the whiteout and hard link rules that docker uses.
package rootfs

import (
	"context"
	"io"
	"io/fs"
	"os"
	"time"
)

// Layer opens the uncompressed tar stream of one layer. Merge can call it
// two times for the same layer.
type Layer func() (io.ReadCloser, error)

// Option configures Merge.
type Option func(*options)

type options struct {
	spoolDir  string
	spoolFile *os.File
	align     int64
	inMemory  bool
}

// WithSpoolDir sets the directory of the temporary spool file. The default
// is os.TempDir().
func WithSpoolDir(dir string) Option { return func(o *options) { o.spoolDir = dir } }

// WithSpoolFile makes Merge write the file data to f, from offset 0. The
// caller owns f, and FS.Close does not close it.
func WithSpoolFile(f *os.File) Option { return func(o *options) { o.spoolFile = f } }

// WithAlign aligns the data of each regular file in the spool to n bytes,
// for example to the EROFS block size.
func WithAlign(n int64) Option { return func(o *options) { o.align = n } }

// InMemory keeps the file data in memory and not in a spool file.
func InMemory() Option { return func(o *options) { o.inMemory = true } }

// Inode is the FileInfo.Sys() value of all entries. Hard links share one
// Inode, and its getters match the go-archivefs interfaces.
type Inode struct {
	Ino          uint64
	Nlink        uint32
	UID, GID     uint32
	Major, Minor uint32            // device numbers of char and block devices
	Xattrs       map[string]string // nil when there are no xattrs

	mode   fs.FileMode
	size   int64
	mtime  time.Time
	target string // symlink target
	off    int64  // spool offset
	data   []byte // data in memory
	rank   int64  // rank of the header that last set uid, gid and mtime
	xs     []xattr
	fwd    *Inode // set when two pending inodes become one
	state  uint8
	refIdx int32 // entry index to read again (state stRefetch)
}

// GetIno implements archivefs.InodeInfo.
func (i *Inode) GetIno() uint64 { return i.Ino }

// GetNlink implements archivefs.InodeInfo.
func (i *Inode) GetNlink() uint64 { return uint64(i.Nlink) }

// GetUID implements archivefs.OwnerInfo.
func (i *Inode) GetUID() int { return int(i.UID) }

// GetGID implements archivefs.OwnerInfo.
func (i *Inode) GetGID() int { return int(i.GID) }

// GetDevMajor implements archivefs.DevInfo.
func (i *Inode) GetDevMajor() uint32 { return i.Major }

// GetDevMinor implements archivefs.DevInfo.
func (i *Inode) GetDevMinor() uint32 { return i.Minor }

// FS is the merged file system. It is safe for concurrent reads.
type FS struct {
	root  *node
	spool *os.File
	own   bool
}

// Merge reads the layers, base layer first, and returns the merged tree.
// It writes only the data of files that are visible in the result.
func Merge(ctx context.Context, layers []Layer, opts ...Option) (*FS, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	m, err := newMerger(layers, o)
	if err != nil {
		return nil, err
	}
	if err := m.run(ctx); err != nil {
		m.close()
		return nil, err
	}
	return &FS{root: m.root, spool: m.spoolF, own: m.ownSpool}, nil
}

// DataOffset returns the spool offset of the data of the regular file at
// name. It returns false for other types and with InMemory.
func (f *FS) DataOffset(name string) (int64, bool) {
	n, err := f.lookup("dataoffset", name, false)
	if err != nil || n.ino.mode.Type() != 0 || f.spool == nil {
		return 0, false
	}
	return n.ino.off, true
}

// Close closes the spool file when Merge created it.
func (f *FS) Close() error {
	if f.spool != nil && f.own {
		return f.spool.Close()
	}
	return nil
}
