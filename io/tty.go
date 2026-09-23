// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !js

package io

import (
	"io"
	"os"
	"strconv"
	"syscall"

	"github.com/charmbracelet/x/term"
)

func withFd(v any, fn func(fd uintptr)) bool {
	if c, ok := v.(interface {
		SyscallConn() (syscall.RawConn, error)
	}); ok {
		raw, err := c.SyscallConn()
		if err != nil {
			return false
		}
		return raw.Control(fn) == nil
	}
	if f, ok := v.(interface{ Fd() uintptr }); ok {
		fn(f.Fd())
		return true
	}
	return false
}

func isTerminal(v any) bool {
	var tty bool
	return withFd(v, func(fd uintptr) { tty = term.IsTerminal(fd) }) && tty
}

// IsTTYWriter reports whether the writer ultimately targets a terminal.
func IsTTYWriter(w io.Writer) bool {
	return isTerminal(Unwrap(w))
}

// IsTTYReader reports whether the reader ultimately draws from a terminal.
func IsTTYReader(r io.Reader) bool {
	return isTerminal(r)
}

// TermWidth returns the terminal width for the writer.
func TermWidth(w io.Writer) int {
	if cols, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil {
		return cols
	}
	var width int
	withFd(Unwrap(w), func(fd uintptr) { width, _, _ = term.GetSize(fd) })
	if width > 0 {
		return width
	}
	return 80
}
