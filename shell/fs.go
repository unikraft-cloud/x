// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"mvdan.cc/sh/v3/interp"

	xio "unikraft.com/x/io"
)

const (
	probeDir = "/"

	writeAck = "ok\n"
)

var errNoShell = errors.New("the instance has no sh; file tests, globs and redirections need one")

func (s *session) statHandler(ctx context.Context, name string, followSymlinks bool) (fs.FileInfo, error) {
	return s.stat(ctx, interp.HandlerCtx(ctx).Dir, name, followSymlinks)
}

func (s *session) stat(ctx context.Context, dir, name string, followSymlinks bool) (fs.FileInfo, error) {
	p := resolve(dir, name)

	lstat := ""
	if !followSymlinks {
		lstat = "1"
	}

	out, err := s.script(ctx, statScript, p, lstat)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: p, Err: err}
	}

	fields := strings.Fields(out)
	if len(fields) < 3 {
		return nil, &fs.PathError{Op: "stat", Path: p, Err: fmt.Errorf("unexpected stat output %q", out)}
	}

	info := remoteFileInfo{name: path.Base(p), kind: fields[0]}
	info.size, _ = strconv.ParseInt(fields[1], 10, 64)
	info.mtime, _ = strconv.ParseInt(fields[2], 10, 64)
	if len(fields) > 3 {
		info.special = fields[3]
	}
	return info, nil
}

func (s *session) accessHandler(ctx context.Context, name string, mode interp.AccessMode) error {
	return s.access(ctx, interp.HandlerCtx(ctx).Dir, name, mode)
}

func (s *session) access(ctx context.Context, dir, name string, mode interp.AccessMode) error {
	p := resolve(dir, name)

	var tests []string
	if mode&interp.AccessRead != 0 {
		tests = append(tests, "-r")
	}
	if mode&interp.AccessWrite != 0 {
		tests = append(tests, "-w")
	}
	if mode&interp.AccessExec != 0 {
		tests = append(tests, "-x")
	}

	out, err := s.script(ctx, accessScript, append([]string{p}, tests...)...)
	if err != nil {
		return &fs.PathError{Op: "access", Path: p, Err: err}
	}

	switch strings.ToLower(strings.TrimSpace(out)) {
	case "ok":
		return nil
	case "missing":
		return &fs.PathError{Op: "access", Path: p, Err: fs.ErrNotExist}
	default:
		return &fs.PathError{Op: "access", Path: p, Err: fs.ErrPermission}
	}
}

func (s *session) readDirHandler(ctx context.Context, name string) ([]fs.DirEntry, error) {
	return s.readDir(ctx, interp.HandlerCtx(ctx).Dir, name)
}

func (s *session) readDir(ctx context.Context, dir, name string) ([]fs.DirEntry, error) {
	p := resolve(dir, name)

	out, err := s.script(ctx, readDirScript, p)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: p, Err: err}
	}

	status, listing, _ := strings.Cut(out, "\n")
	switch strings.TrimSpace(status) {
	case "ok":
	case "notdir":
		return nil, &fs.PathError{Op: "readdir", Path: p, Err: syscall.ENOTDIR}
	case "denied":
		return nil, &fs.PathError{Op: "readdir", Path: p, Err: fs.ErrPermission}
	case "missing":
		return nil, &fs.PathError{Op: "readdir", Path: p, Err: fs.ErrNotExist}
	default:
		return nil, &fs.PathError{Op: "readdir", Path: p, Err: fmt.Errorf("unexpected readdir output %q", status)}
	}

	var entries []fs.DirEntry
	for record := range strings.SplitSeq(listing, "\x00") {
		kind, entry, ok := strings.Cut(record, " ")
		if !ok || entry == "" {
			continue
		}
		entries = append(entries, remoteDirEntry{remoteFileInfo{name: entry, kind: kind}})
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

func (s *session) open(ctx context.Context, name string, flag int, _ os.FileMode) (io.ReadWriteCloser, error) {
	if name == os.DevNull {
		return xio.DevNull, nil
	}
	hc := interp.HandlerCtx(ctx)
	p := resolve(hc.Dir, name)

	// A remote file is a one-way stream: read with cat, write with a
	// redirection so fail <>
	if flag&os.O_RDWR != 0 {
		return nil, &fs.PathError{Op: "open", Path: p, Err: errors.ErrUnsupported}
	}
	if flag&(os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_TRUNC) == 0 {
		return s.openRead(ctx, p, hc.Stderr)
	}
	return s.openWrite(ctx, p, flag&os.O_APPEND != 0, hc.Stderr)
}

func (s *session) openRead(ctx context.Context, p string, stderr io.Writer) (io.ReadWriteCloser, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: p, Err: err}
	}

	ctx, cancel := context.WithCancel(ctx)
	out := &firstByte{w: pw, seen: make(chan error, 1)}
	go func() {
		err := s.redirect(ctx, "open", p, readScript, Streams{Out: out})
		_ = pw.Close()
		cancel()
		if !out.announce(err) && err != nil && !out.gone.Load() {
			fmt.Fprintln(stderr, errorStyle.Render(err.Error()))
		}
	}()

	if err := <-out.seen; err != nil {
		_ = pr.Close()
		return nil, err
	}
	return pr, nil
}

// firstByte tells the opener the moment the helper has something to say: its
// first byte, or how it ended without one.
type firstByte struct {
	w    io.Writer
	once sync.Once
	seen chan error
	gone atomic.Bool
}

func (f *firstByte) Write(p []byte) (int, error) {
	f.announce(nil)
	n, err := f.w.Write(p)
	if err != nil {
		f.gone.Store(true)
	}
	return n, err
}

// announce reports err as the outcome of the open, unless one was already announced.
func (f *firstByte) announce(err error) (first bool) {
	f.once.Do(func() {
		first = true
		f.seen <- err
	})
	return first
}

func (s *session) openWrite(ctx context.Context, p string, appending bool, stderr io.Writer) (io.ReadWriteCloser, error) {
	snippet := writeScript
	if appending {
		snippet = appendScript
	}

	pr, pw := io.Pipe()
	ackR, ackW := io.Pipe()
	w := &remoteWriter{w: pw, stderr: stderr, done: make(chan error, 1)}
	go func() {
		err := s.redirect(ctx, "write", p, snippet, Streams{In: pr, Out: ackW})
		_ = pr.CloseWithError(err)
		_ = ackW.CloseWithError(err)
		w.done <- err
	}()

	ack := make([]byte, len(writeAck))
	if _, err := io.ReadFull(ackR, ack); err != nil || string(ack) != writeAck {
		_ = pw.Close()
		switch failed := <-w.done; {
		case ctx.Err() != nil:
			return xio.DevNull, nil
		case failed != nil:
			return nil, failed
		default:
			return nil, &fs.PathError{Op: "write", Path: p, Err: fs.ErrInvalid}
		}
	}

	go func() { _, _ = io.Copy(io.Discard, ackR) }()
	return w, nil
}

// redirect runs a helper that streams a file, and reports how it went: nothing
// when the statement itself was interrupted, the helper's own words otherwise.
func (s *session) redirect(ctx context.Context, op, p, snippet string, streams Streams) error {
	if s.noShell {
		return &fs.PathError{Op: op, Path: p, Err: errNoShell}
	}

	var errOut bytes.Buffer
	streams.Err = &errOut

	code, err := s.cfg.Transport.Exec(ctx, streams, probeDir, nil,
		[]string{"sh", "-c", snippet, "sh", p})
	switch {
	case err != nil:
		return &fs.PathError{Op: op, Path: p, Err: err}
	case ctx.Err() != nil:
		return nil
	case code < 0:
		return &fs.PathError{Op: op, Path: p, Err: fmt.Errorf("the helper was signalled (%d)", -code)}
	case code != 0:
		return &fs.PathError{Op: op, Path: p, Err: redirectError(&errOut)}
	default:
		return nil
	}
}

func redirectError(errOut *bytes.Buffer) error {
	said := strings.TrimSpace(errOut.String())
	if _, detail, ok := strings.Cut(said, ": "); ok {
		said = detail
	}
	if said == "" {
		return fs.ErrInvalid
	}
	return errors.New(said)
}

// script runs a probe and returns what it printed. A probe that exits non-zero
// without a word is asking about something that is not there; one with a
// complaint is failing.
func (s *session) script(ctx context.Context, snippet string, args ...string) (string, error) {
	if s.noShell {
		return "", errNoShell
	}

	var out, errOut bytes.Buffer

	ctx = Detached(ctx)

	code, err := s.cfg.Transport.Exec(ctx, Streams{Out: &out, Err: &errOut}, probeDir, nil,
		append([]string{"sh", "-c", snippet, "sh"}, args...))
	switch {
	case err != nil:
		return "", err
	case code < 0:
		return "", fmt.Errorf("the probe was signalled (%d)", -code)
	case code != 0:
		if said := strings.TrimSpace(errOut.String()); said != "" {
			return "", errors.New(said)
		}
		return "", fs.ErrNotExist
	default:
		return out.String(), nil
	}
}
