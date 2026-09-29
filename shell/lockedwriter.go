// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"io"
	"sync"

	xio "unikraft.com/x/io"
)

// lockWriter serialises writes to w from commands running at the same time,
// keeping w's underlying terminal Fd visible through the lock.
func lockWriter(mu *sync.Mutex, w io.Writer) io.Writer {
	return xio.WithFd(&lockedWriter{mu: mu, w: w}, w)
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
