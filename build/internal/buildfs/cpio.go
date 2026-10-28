// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2024, Unikraft GmbH and The KraftKit Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package buildfs

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	gopath "path"

	"github.com/unikraft/go-archivefs"
	"github.com/unikraft/go-cpio"
	"unikraft.com/x/log"
)

// CreateCPIO creates a CPIO filesystem from the given fs.FS.
func CreateCPIO(ctx context.Context, w io.Writer, fsys fs.FS, opts ...CreateOption) error {
	c := createOptions{}
	for _, opt := range opts {
		opt(&c)
	}

	cw := cpio.NewWriter(w)
	defer cw.Close()

	inodes := newInodeAllocator()
	hardlinks := map[hardlinkKey]string{}

	// Recursively walk and serialize to the output
	if err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("received error before parsing path: %w", err)
		}

		if path == "." {
			return nil // Do not archive the root itself
		}
		internal := "./" + path

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("could not get directory entry info: %w", err)
		}

		if d.Type().IsDir() {
			header := &cpio.Header{
				Name:    internal,
				Mode:    cpioMode(info.Mode()) | cpio.TypeDir,
				ModTime: info.ModTime(),
				Size:    0, // Directories have size 0 in cpio
			}

			// Populate platform specific information
			fileInfoToCPIOHeader(info, header)
			applyFileInfoMap(&c, path, header)
			canonicalizeInode(inodes, header)

			if c.allRoot {
				header.Uid = 0
				header.Guid = 0
			}

			if err := cw.WriteHeader(header); err != nil {
				return fmt.Errorf("could not write CPIO header: %w", err)
			}
			return nil
		}

		log.G(ctx).
			Debug().
			Str("file", internal).
			Msg("archiving")

		header := &cpio.Header{
			Name:    internal,
			Mode:    cpioMode(info.Mode()),
			ModTime: info.ModTime(),
			Size:    info.Size(),
		}

		// Populate platform specific information
		fileInfoToCPIOHeader(info, header)
		applyFileInfoMap(&c, path, header)
		source, isHardlink := canonicalizeInode(inodes, header)

		if c.allRoot {
			header.Uid = 0
			header.Guid = 0
		}

		var data []byte
		var content bool // of a regular file, copied after the header
		switch {
		case info.Mode().IsRegular():
			header.Mode |= cpio.TypeRegular
			if isHardlink {
				if _, ok := hardlinks[source]; ok {
					header.Size = 0
					break
				}
				hardlinks[source] = internal
			}
			content = true

		case info.Mode()&fs.ModeSymlink != 0:
			targetLink, err := fs.ReadLink(fsys, path)
			if err != nil {
				return fmt.Errorf("could not read symlink: %w", err)
			}
			data = []byte(targetLink)
			header.Mode |= cpio.TypeSymlink
			header.Linkname = targetLink
			header.Size = int64(len(data))

		default:
			log.G(ctx).Warn().Msgf("unsupported file: %s", path)
			return nil
		}

		if err := cw.WriteHeader(header); err != nil {
			return fmt.Errorf("writing cpio header for %q: %w", internal, err)
		}

		if content {
			if err := copyFile(cw, fsys, path, header.Size); err != nil {
				return fmt.Errorf("could not write CPIO data for %s: %w", internal, err)
			}
		}
		if len(data) > 0 {
			if _, err := cw.Write(data); err != nil {
				return fmt.Errorf("could not write CPIO data for %s: %w", internal, err)
			}
		}

		return nil
	}); err != nil {
		return fmt.Errorf("could not walk output path: %w", err)
	}

	return nil
}

// hardlinkKey identifies a file on the source filesystem.
type hardlinkKey struct {
	device int
	inode  int64
}

// inodeAllocator gives archive inode numbers in walk order, so that the same
// tree gives the same archive on all hosts.
type inodeAllocator struct {
	next   int64
	groups map[hardlinkKey]int64
}

func newInodeAllocator() *inodeAllocator {
	return &inodeAllocator{next: 1, groups: map[hardlinkKey]int64{}}
}

// get returns the archive inode for source. All links to one file get the
// same inode.
func (a *inodeAllocator) get(source hardlinkKey, linked bool) int64 {
	if linked {
		if inode, ok := a.groups[source]; ok {
			return inode
		}
	}

	inode := a.next
	a.next++

	if linked {
		a.groups[source] = inode
	}

	return inode
}

// canonicalizeInode replaces the source inode on header with an archive inode.
// It returns the source identity and true if the entry is one of many links.
func canonicalizeInode(a *inodeAllocator, header *cpio.Header) (hardlinkKey, bool) {
	source := hardlinkKey{device: header.DeviceID, inode: header.Inode}
	linked := header.Links > 1 && header.Inode != 0
	header.Inode = a.get(source, linked)

	return source, linked
}

// applyFileInfoMap sets the owner and mode of path from the WithFileInfoMap
// entry. It keeps the type bits.
func applyFileInfoMap(c *createOptions, path string, header *cpio.Header) {
	if c.fileInfoMap == nil {
		return
	}

	info, ok := c.fileInfoMap[gopath.Join("/", path)]
	if !ok {
		return
	}

	header.Uid = info.Uid
	header.Guid = info.Gid
	header.Mode = header.Mode&^cpio.ModePerm | cpio.FileMode(info.Mode.Perm())
}

// cpioMode returns the permission bits of mode, with setuid, setgid and
// sticky, as cpio mode bits.
func cpioMode(mode fs.FileMode) cpio.FileMode {
	m := cpio.FileMode(mode.Perm())
	if mode&fs.ModeSetuid != 0 {
		m |= cpio.ModeSetuid
	}
	if mode&fs.ModeSetgid != 0 {
		m |= cpio.ModeSetgid
	}
	if mode&fs.ModeSticky != 0 {
		m |= cpio.ModeSticky
	}
	return m
}

func fileInfoToCPIOHeader(info fs.FileInfo, header *cpio.Header) {
	sys := info.Sys()
	header.Uid = archivefs.GetUID(sys)
	header.Guid = archivefs.GetGID(sys)
	header.Inode = int64(archivefs.GetIno(sys))
	header.Links = int(archivefs.GetNlink(sys))
}

// copyFile copies the size bytes of the file at path in fsys to w.
func copyFile(w io.Writer, fsys fs.FS, path string, size int64) error {
	f, err := fsys.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(w, io.LimitReader(f, size))
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("read %d bytes, not %d", n, size)
	}
	return nil
}
