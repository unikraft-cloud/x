// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package io

import "io"

// Flusher is implemented by writers that buffer output until told to flush it.
type Flusher interface {
	Flush() error
}

// WriteFlusher is a Writer that also flushes.
type WriteFlusher interface {
	io.Writer
	Flusher
}
