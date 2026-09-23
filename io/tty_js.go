// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build js

package io

import (
	"io"
	"os"
	"strconv"
)

// IsTTYWriter reports whether the writer ultimately targets a terminal.
func IsTTYWriter(w io.Writer) bool {
	term := os.Getenv("TERM")
	return term != "" && term != "dumb"
}

// IsTTYReader reports whether the reader ultimately draws from a terminal.
func IsTTYReader(r io.Reader) bool {
	// The wasm isolate has no TTY. The host terminal shows ANSI output, but
	// stdin is only a byte stream that has no termios controls.
	return false
}

// TermWidth returns the terminal width for the writer.
func TermWidth(w io.Writer) int {
	if cols, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil {
		return cols
	}
	return 80
}
