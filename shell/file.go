// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"fmt"
	"io"
	"io/fs"
	"math"
	"syscall"
	"time"
)

// unknownOwner is the uid/gid remote files report: the probe never asks
// who owns them, and 2^32-1 cannot be mistaken for a real user.
const unknownOwner = math.MaxUint32

// remoteReader streams a file off the instance, head first: what the open had to read.
type remoteReader struct {
	r    *io.PipeReader
	head []byte
}

func (f *remoteReader) Read(p []byte) (int, error) {
	if len(f.head) > 0 {
		n := copy(p, f.head)
		f.head = f.head[n:]
		return n, nil
	}
	return f.r.Read(p)
}

func (f *remoteReader) Write([]byte) (int, error) { return 0, fs.ErrInvalid }

func (f *remoteReader) Close() error { return f.r.Close() }

// remoteWriter streams a redirection onto the instance.
type remoteWriter struct {
	w    *io.PipeWriter
	s    *session
	done chan error
}

func (f *remoteWriter) Read([]byte) (int, error) { return 0, fs.ErrInvalid }

func (f *remoteWriter) Write(p []byte) (int, error) { return f.w.Write(p) }

func (f *remoteWriter) Close() error {
	_ = f.w.Close()

	err := <-f.done
	if err != nil {
		fmt.Fprintln(f.s.console.Err, errorStyle.Render(err.Error()))
	}
	return err
}

type devNull struct{}

func (devNull) Read([]byte) (int, error)    { return 0, io.EOF }
func (devNull) Write(p []byte) (int, error) { return len(p), nil }
func (devNull) Close() error                { return nil }

// remoteFileInfo answers what statScript reported, kind being its one-letter file type.
type remoteFileInfo struct {
	name    string
	size    int64
	kind    string
	special string
}

func (f remoteFileInfo) Name() string { return f.name }
func (f remoteFileInfo) Size() int64  { return f.size }
func (f remoteFileInfo) IsDir() bool  { return f.kind == "d" }

func (f remoteFileInfo) Sys() any { return &syscall.Stat_t{Uid: unknownOwner, Gid: unknownOwner} }

func (f remoteFileInfo) ModTime() time.Time { return time.Time{} }

func (f remoteFileInfo) Mode() fs.FileMode {
	var mode fs.FileMode
	switch f.kind {
	case "d":
		mode = fs.ModeDir | 0o755
	case "L":
		mode = fs.ModeSymlink | 0o777
	case "p":
		mode = fs.ModeNamedPipe | 0o644
	case "S":
		mode = fs.ModeSocket | 0o644
	case "b":
		mode = fs.ModeDevice | 0o644
	case "c":
		mode = fs.ModeDevice | fs.ModeCharDevice | 0o644
	case "f":
		mode = 0o644
	default:
		mode = fs.ModeIrregular | 0o644
	}

	for _, bit := range f.special {
		switch bit {
		case 'u':
			mode |= fs.ModeSetuid
		case 'g':
			mode |= fs.ModeSetgid
		case 'k':
			mode |= fs.ModeSticky
		}
	}
	return mode
}

type remoteDirEntry struct{ info remoteFileInfo }

func (e remoteDirEntry) Name() string               { return e.info.Name() }
func (e remoteDirEntry) IsDir() bool                { return e.info.IsDir() }
func (e remoteDirEntry) Type() fs.FileMode          { return e.info.Mode().Type() }
func (e remoteDirEntry) Info() (fs.FileInfo, error) { return e.info, nil }
