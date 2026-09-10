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

	"github.com/muesli/cancelreader"
)

// terminalStdin lends tty to one command: the reader is its stdin until ctx ends
// or reclaim is called, whichever comes first, and reclaim returns only once no
// read is left in flight on the terminal.
func terminalStdin(ctx context.Context, tty *os.File) (in io.Reader, reclaim func()) {
	reader, err := cancelreader.NewReader(tty)
	if err != nil {
		return tty, func() {}
	}

	terminal := &reclaimableTerminal{reader: reader}
	go func() {
		<-ctx.Done()
		terminal.reclaim()
	}()

	return terminal, terminal.reclaim
}

// reclaimableTerminal reads the terminal on behalf of one command and hands it
// back on demand: reclaim interrupts even a blocked read, and every read after
// it reports EOF.
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
