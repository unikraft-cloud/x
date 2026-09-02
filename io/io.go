// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package io provides helpers that complement the standard library's io.
package io

import stdio "io"

// NopWriteCloser returns a WriteCloser whose Close is a no-op, for handing a
// writer to an API that would otherwise close it.
func NopWriteCloser(w stdio.Writer) stdio.WriteCloser {
	return nopWriteCloser{w}
}

type nopWriteCloser struct {
	stdio.Writer
}

func (nopWriteCloser) Close() error { return nil }
