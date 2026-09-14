// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"github.com/muesli/cancelreader"
)

// lendFile lends the session's own input to one command
func lendFile(ctx context.Context, f *os.File) (in io.Reader, reclaim func()) {
	lent := reclaimable(f)
	if lent == nil {
		return f, func() {}
	}

	go func() {
		<-ctx.Done()
		lent.reclaim()
	}()

	return lent, lent.reclaim
}

// lent is a command's input for as long as it runs.
type lent interface {
	io.Reader
	reclaim()
}

// reclaimable is how a file is taken back off a command
func reclaimable(f *os.File) lent {
	if f.SetReadDeadline(time.Time{}) == nil {
		return &reclaimableFile{f: f}
	}

	reader, err := cancelreader.NewReader(f)
	if err != nil {
		return nil
	}
	return &reclaimableTerminal{reader: reader}
}

// reclaimableFile reads the session's input on behalf of one command, and takes
// it back with a deadline the read is already past
type reclaimableFile struct {
	f *os.File

	mu   sync.RWMutex
	done bool
	once sync.Once
}

func (r *reclaimableFile) Read(b []byte) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.done {
		return 0, io.EOF
	}

	n, err := r.f.Read(b)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return n, io.EOF
	}
	return n, err
}

func (r *reclaimableFile) reclaim() {
	r.once.Do(func() {
		_ = r.f.SetReadDeadline(time.Now())

		r.mu.Lock()
		defer r.mu.Unlock()

		r.done = true
		// The file is the session's and not this command's: leave it as it was.
		_ = r.f.SetReadDeadline(time.Time{})
	})
}

// reclaimableTerminal reads the session's input on behalf of one command and
// hands it back on demand
type reclaimableTerminal struct {
	reader cancelreader.CancelReader

	mu   sync.RWMutex
	done bool
	once sync.Once
}

func (t *reclaimableTerminal) Read(b []byte) (int, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.done {
		return 0, io.EOF
	}

	n, err := t.reader.Read(b)
	if errors.Is(err, cancelreader.ErrCanceled) {
		return n, io.EOF
	}
	return n, err
}

func (t *reclaimableTerminal) reclaim() {
	t.once.Do(func() {
		t.reader.Cancel()

		t.mu.Lock()
		defer t.mu.Unlock()

		t.done = true
		_ = t.reader.Close()
	})
}
