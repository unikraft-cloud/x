// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"context"
	"io"
	"io/fs"
	"os"

	"mvdan.cc/sh/v3/interp"
)

// The interpreter asks about the instance's filesystem, not this machine's, so
// every handler below hands the question to the transport.

func (s *state) statHandler(ctx context.Context, name string, followSymlinks bool) (fs.FileInfo, error) {
	dir := interp.HandlerCtx(ctx).Dir
	if err := s.validate("stat", dir, name); err != nil {
		return nil, err
	}

	key := statKey{path: resolve(dir, name), follow: followSymlinks}
	if info, asked := s.recalledStat(key); asked {
		return info, nil
	}

	asOf := s.statsAsOf()
	info, err := s.cfg.Transport.Stat(ctx, dir, name, followSymlinks)
	if err == nil {
		s.rememberStat(key, info, asOf)
	}
	return info, err
}

// statKey is one question about the instance's filesystem
type statKey struct {
	path   string
	follow bool
}

func (s *state) recalledStat(key statKey) (fs.FileInfo, bool) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	info, asked := s.stats[key]
	return info, asked
}

// statsAsOf is what the instance was when a question was put to it; a command
// running beside it in a pipeline moves this on.
func (s *state) statsAsOf() uint64 {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	return s.statsGen
}

func (s *state) rememberStat(key statKey, info fs.FileInfo, asOf uint64) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	if asOf != s.statsGen {
		return
	}
	if s.stats == nil {
		s.stats = map[statKey]fs.FileInfo{}
	}
	s.stats[key] = info
}

func (s *state) forgetStats() {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	clear(s.stats)
	s.statsGen++
}

func (s *state) accessHandler(ctx context.Context, name string, mode interp.AccessMode) error {
	dir := interp.HandlerCtx(ctx).Dir
	if err := s.validate("access", dir, name); err != nil {
		return err
	}
	if s.enterable(dir, name, accessMode(mode)) {
		return nil
	}
	return s.cfg.Transport.Access(ctx, dir, name, accessMode(mode))
}

// enterable is whether root may enter a directory a stat has already described.
func (s *state) enterable(dir, name string, mode AccessMode) bool {
	if mode != AccessExec || s.euid != "0" {
		return false
	}
	info, asked := s.recalledStat(statKey{path: resolve(dir, name), follow: true})
	return asked && info != nil && info.IsDir()
}

func (s *state) readDirHandler(ctx context.Context, name string) ([]fs.DirEntry, error) {
	dir := interp.HandlerCtx(ctx).Dir
	if err := s.validate("readdir", dir, name); err != nil {
		return nil, err
	}
	return s.cfg.Transport.ReadDir(ctx, dir, name)
}

func (s *state) open(ctx context.Context, name string, flag int, _ os.FileMode) (io.ReadWriteCloser, error) {
	hc := interp.HandlerCtx(ctx)
	if name != os.DevNull {
		if err := s.validate("open", hc.Dir, name); err != nil {
			return nil, err
		}
	}
	if flag&(os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_TRUNC) != 0 {
		s.forgetStats()
	}
	return s.cfg.Transport.Open(ctx, hc.Dir, name, flag, hc.Stderr)
}

// validate is a file question the transport has already said it cannot
// answer, the instance having no sh for it to ask with.
func (s *state) validate(op, dir, name string) error {
	if !s.noShell {
		return nil
	}
	return &fs.PathError{Op: op, Path: resolve(dir, name), Err: ErrNoShell}
}

// accessMode is the interpreter's way of asking, in ours.
func accessMode(mode interp.AccessMode) AccessMode {
	var want AccessMode
	if mode&interp.AccessRead != 0 {
		want |= AccessRead
	}
	if mode&interp.AccessWrite != 0 {
		want |= AccessWrite
	}
	if mode&interp.AccessExec != 0 {
		want |= AccessExec
	}
	return want
}
