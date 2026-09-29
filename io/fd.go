// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package io

import "io"

// WithFd returns w augmented to report Fd() when the writer under it has one.
func WithFd(w, under io.Writer) io.Writer {
	if fd, ok := Unwrap(under).(interface{ Fd() uintptr }); ok {
		return &fdWriter{Writer: w, fd: fd}
	}
	return w
}

type fdWriter struct {
	io.Writer
	fd interface{ Fd() uintptr }
}

func (w *fdWriter) Fd() uintptr { return w.fd.Fd() }
