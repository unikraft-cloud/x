// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package mergefs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"time"
)

// Layer opens the uncompressed tar stream of one layer. Merge calls it one
// time, when it starts to read the layer.
type Layer func() (io.ReadCloser, error)

// Option configures Merge.
type Option func(*options)

type options struct {
	spoolDir   string
	spoolFile  *os.File // for tests
	inMemory   bool
	maxSize    int64
	maxMeta    int64
	maxEntries int64
	maxRead    int64
}

var (
	// ErrTooLarge is the error of Merge when the file data is larger than WithMaxSize allows.
	ErrTooLarge = errors.New("rootfs is too large")
	// ErrMetaTooLarge is the error of Merge when the metadata is larger than WithMaxMetaSize allows.
	ErrMetaTooLarge = errors.New("metadata of the rootfs is too large")
	// ErrTooManyEntries is the error of Merge when the layers have more entries than WithMaxEntries allows.
	ErrTooManyEntries = errors.New("rootfs has too many entries")
	// ErrLayersTooLarge is the error of Merge when it reads more from the layers than WithMaxReadSize allows.
	ErrLayersTooLarge = errors.New("layers are too large")
)

// WithSpoolDir sets the directory of the temporary files of Merge: the spool
// and the scratch file. The default is os.TempDir().
func WithSpoolDir(dir string) Option { return func(o *options) { o.spoolDir = dir } }

// InMemory keeps the file data in memory and not in temporary files.
func InMemory() Option { return func(o *options) { o.inMemory = true } }

// WithMaxSize makes Merge fail with ErrTooLarge when it would keep more than n bytes of file data at one time, in its temporary files or in memory.
func WithMaxSize(n int64) Option { return func(o *options) { o.maxSize = n } }

// WithMaxMetaSize makes Merge fail with ErrMetaTooLarge when the names, the xattrs and the link targets of all the entries,
// with the paths of the directories that the names imply, are more than n bytes. They stay in memory, also in a writer of the tree.
func WithMaxMetaSize(n int64) Option { return func(o *options) { o.maxMeta = n } }

// WithMaxEntries makes Merge fail with ErrTooManyEntries when the layers have more than n tar entries, with the directories that their names imply
// and the directories above the hard-link targets that it looks for in lower layers.
func WithMaxEntries(n int64) Option { return func(o *options) { o.maxEntries = n } }

// WithMaxReadSize makes Merge fail with ErrLayersTooLarge when it reads more than n bytes from the uncompressed tar streams of all the layers: the entries, also the ones that it skips, and the data after the end of each archive.
func WithMaxReadSize(n int64) Option { return func(o *options) { o.maxRead = n } }

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
	target string           // symlink target
	off    int64            // spool offset
	data   []byte           // data in memory
	rank   int64            // rank of the header that last set uid, gid and mtime
	xs     map[string]xattr // the kept xattr of each name
	xlist  int              // bytes of the names in xs, as listxattr counts them
	fwd    *Inode           // set when two pending inodes become one
	state  state
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

// GetXattrs implements archivefs.XattrInfo.
func (i *Inode) GetXattrs() (map[string]string, error) { return i.Xattrs, nil }

// Merge takes the layers base layer first, reads each one time from the top
// layer down, and returns the merged tree. It writes to the spool only the
// data of the files that stay visible.
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
	return &FS{root: m.root, spool: m.spoolF, own: m.ownSpool, left: m.spoolLeft}, nil
}
