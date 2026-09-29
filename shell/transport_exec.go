// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"bytes"
	"cmp"
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

	xio "unikraft.com/x/io"
	"unikraft.com/x/log"
	"unikraft.com/x/stdio"
)

const (
	probeDir = "/"

	// writeAck is the line the write helpers print once the file is open.
	writeAck = "ok"

	// statFields is the record statScript prints: kind, size, mtime, special.
	statFields = 4
)

// ErrNoShell is what an instance without sh answers: file tests, globs and
// redirections need one.
var ErrNoShell = errors.New("the instance has no sh; file tests, globs and redirections need one")

// probing bounds a question nobody typed and nobody waits for, which has no ^C
// to fall back on.
func probing(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, instanceProbeTimeout)
	return WithDetached(ctx), cancel
}

// ExecTransport answers all of it through sh on the instance, given only a way
// to run a command.
type ExecTransport func(ctx context.Context, cmd Command) (int, error)

func (e ExecTransport) Exec(ctx context.Context, cmd Command) (int, error) {
	return e(ctx, cmd)
}

func (e ExecTransport) Stat(ctx context.Context, dir, name string, followSymlinks bool) (fs.FileInfo, error) {
	p := resolve(dir, name)

	lstat := ""
	if !followSymlinks {
		lstat = "1"
	}

	out, err := e.script(ctx, statScript, p, lstat)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: p, Err: err}
	}

	// The record is the last line: whatever the instance's sh said first is not it.
	record := strings.TrimSpace(out)
	if i := strings.LastIndexByte(record, '\n'); i >= 0 {
		record = record[i+1:]
	}
	fields := strings.Fields(record)
	if len(fields) != statFields {
		return nil, &fs.PathError{Op: "stat", Path: p, Err: fmt.Errorf("unexpected stat output %q", out)}
	}

	info := remoteFileInfo{name: path.Base(p), kind: fields[0]}
	info.size, _ = strconv.ParseInt(fields[1], 10, 64)
	info.mtime, _ = strconv.ParseInt(fields[2], 10, 64)
	if fields[3] != "-" {
		info.special = fields[3]
	}
	return info, nil
}

func (e ExecTransport) Access(ctx context.Context, dir, name string, mode AccessMode) error {
	p := resolve(dir, name)

	var tests []string
	if mode&AccessRead != 0 {
		tests = append(tests, "-r")
	}
	if mode&AccessWrite != 0 {
		tests = append(tests, "-w")
	}
	if mode&AccessExec != 0 {
		tests = append(tests, "-x")
	}

	out, err := e.script(ctx, accessScript, append([]string{p}, tests...)...)
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

func (e ExecTransport) ReadDir(ctx context.Context, dir, name string) ([]fs.DirEntry, error) {
	p := resolve(dir, name)

	out, err := e.script(ctx, readDirScript, p)
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

// Open streams a file one way: a remote file is read with cat and written with
// a redirection, so <> is refused.
func (e ExecTransport) Open(ctx context.Context, dir, name string, flag int, stderr io.Writer) (io.ReadWriteCloser, error) {
	if name == os.DevNull {
		return xio.DevNull, nil
	}
	p := resolve(dir, name)

	if flag&os.O_RDWR != 0 {
		return nil, &fs.PathError{Op: "open", Path: p, Err: errors.ErrUnsupported}
	}
	if flag&(os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_TRUNC) == 0 {
		return e.openRead(ctx, p, stderr)
	}
	return e.openWrite(ctx, p, flag&os.O_APPEND != 0, stderr)
}

// Environ reads what the instance exports, and says ErrNoShell when there is
// no sh to read it with.
func (e ExecTransport) Environ(ctx context.Context) ([]string, error) {
	var err error
	for range instanceProbeAttempts {
		probeCtx, cancel := probing(ctx)
		out := cappedBuffer{limit: maxProbeOutput, stop: cancel}

		var code int
		code, err = e(probeCtx, Command{
			Args:    []string{"sh", "-c", environProbe},
			Dir:     probeDir,
			Streams: stdio.Stdio{Stdout: &out, Stderr: io.Discard},
		})
		cancel()

		switch {
		case out.full:
			return nil, fmt.Errorf("the instance answered with more than %d MiB", maxProbeOutput>>20)
		case err == nil && code == statusBuiltinNotFound:
			return nil, ErrNoShell
		case err == nil:
			return environOf(out.String()), nil
		case ctx.Err() != nil:
			return nil, sanitised(err)
		}
	}
	return nil, sanitised(err)
}

// environOf is the NUL-separated record the environ probe prints.
func environOf(out string) []string {
	var env []string
	for record := range strings.SplitSeq(out, "\x00") {
		if name, _, ok := strings.Cut(record, "="); ok && isEnvName(name) {
			env = append(env, record)
		}
	}
	return env
}

func isEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

func (e ExecTransport) openRead(ctx context.Context, p string, stderr io.Writer) (io.ReadWriteCloser, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: p, Err: err}
	}

	ctx, cancel := context.WithCancel(ctx)
	out := &firstByte{w: pw, seen: make(chan error, 1)}
	go func() {
		err := e.redirect(ctx, "open", p, readScript, stdio.Stdio{Stdout: out})
		_ = pw.Close()
		cancel()
		if !out.announce(err) && err != nil && !out.gone.Load() {
			log.G(ctx).Debug().Err(err).Msg("the redirection failed after it opened")
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

func (e ExecTransport) openWrite(ctx context.Context, p string, appending bool, stderr io.Writer) (io.ReadWriteCloser, error) {
	snippet := writeScript
	if appending {
		snippet = appendScript
	}

	pr, pw := io.Pipe()
	ack := &ackWriter{seen: make(chan error, 1)}
	w := &remoteWriter{w: pw, stderr: stderr, log: log.G(ctx), done: make(chan error, 1)}
	go func() {
		err := e.redirect(ctx, "write", p, snippet, stdio.Stdio{Stdin: pr, Stdout: ack})
		_ = pr.CloseWithError(err)
		// A helper that ended without a word never opened the file.
		ack.announce(cmp.Or(err, error(fs.ErrInvalid)))
		w.done <- err
	}()

	if err := <-ack.seen; err != nil {
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
	return w, nil
}

// ackWriter listens for the write helper's first line, the acknowledgement that
// the file is open
type ackWriter struct {
	mu   sync.Mutex
	line []byte
	done bool
	once sync.Once
	seen chan error
}

func (a *ackWriter) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.done {
		return len(p), nil
	}
	a.line = append(a.line, p...)
	if i := bytes.IndexByte(a.line, '\n'); i >= 0 || len(a.line) > len(writeAck)+len("\r\n") {
		a.done = true
		if strings.TrimRight(string(a.line), "\r\n") == writeAck {
			a.announce(nil)
		} else {
			a.announce(fs.ErrInvalid)
		}
	}
	return len(p), nil
}

// announce reports err as the outcome of the open, unless one was already announced.
func (a *ackWriter) announce(err error) {
	a.once.Do(func() { a.seen <- err })
}

// redirect runs a helper that streams a file, and reports any errors that may
// occur
func (e ExecTransport) redirect(ctx context.Context, op, p, snippet string, streams stdio.Stdio) error {
	errOut := cappedBuffer{limit: maxProbeOutput}
	streams.Stderr = &errOut

	code, err := e(ctx, Command{
		Args:    []string{"sh", "-c", snippet, "sh", p},
		Dir:     probeDir,
		Streams: streams,
	})
	switch {
	case err != nil:
		return &fs.PathError{Op: op, Path: p, Err: sanitised(err)}
	case ctx.Err() != nil:
		return nil
	case code < 0:
		return &fs.PathError{Op: op, Path: p, Err: fmt.Errorf("the helper was signalled (%d)", -code)}
	case code != 0:
		return &fs.PathError{Op: op, Path: p, Err: redirectError(errOut.String())}
	default:
		return nil
	}
}

func redirectError(errOut string) error {
	said := strings.TrimSpace(errOut)
	if _, detail, ok := strings.Cut(said, ": "); ok {
		said = detail
	}
	if said == "" {
		return fs.ErrInvalid
	}
	return errors.New(said)
}

// maxProbeOutput is how much of a helper's output the shell will hold.
const maxProbeOutput = 1 << 22

// script runs a helper on the context it is given, and returns what it printed
func (e ExecTransport) script(ctx context.Context, snippet string, args ...string) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	out := cappedBuffer{limit: maxProbeOutput, stop: cancel}
	errOut := cappedBuffer{limit: maxProbeOutput, stop: cancel}

	code, err := e(ctx, Command{
		Args:    append([]string{"sh", "-c", snippet, "sh"}, args...),
		Dir:     probeDir,
		Streams: stdio.Stdio{Stdout: &out, Stderr: &errOut},
	})
	switch {
	case out.full || errOut.full:
		return "", fmt.Errorf("the instance answered with more than %d MiB", maxProbeOutput>>20)
	case err != nil:
		return "", sanitised(err)
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

// cappedBuffer keeps what a helper printed, and no more than limit of it.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
	stop  context.CancelFunc
	full  bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	room := c.limit - c.buf.Len()
	if len(p) <= room {
		return c.buf.Write(p)
	}
	if !c.full {
		c.full = true
		if c.stop != nil {
			c.stop()
		}
	}
	if room > 0 {
		_, _ = c.buf.Write(p[:room])
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return c.buf.String() }
