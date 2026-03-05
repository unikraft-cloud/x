// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build (!appengine && linux) || freebsd || darwin || dragonfly || netbsd || openbsd

package guesstermwidth

import (
	"io"
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

// IsTTY checks if the given writer is a terminal.
func IsTTY(w io.Writer) bool {
	if t, ok := w.(interface{ Fd() uintptr }); ok {
		fd := t.Fd()
		var val syscall.Termios
		_, _, err := syscall.Syscall(
			syscall.SYS_IOCTL,
			uintptr(fd),
			uintptr(termiosIoctlGet),
			uintptr(unsafe.Pointer(&val)),
		)
		return err == 0
	}
	return false
}

// GuessTermWidth guesses the terminal width based on the COLUMNS environment
// variable or by querying the terminal's window size using the ioctl system call.
func GuessTermWidth(w io.Writer) int {
	// check if COLUMNS env is set to comply with
	// http://pubs.opengroup.org/onlinepubs/009604499/basedefs/xbd_chap08.html
	colsStr := os.Getenv("COLUMNS")
	if colsStr != "" {
		if cols, err := strconv.Atoi(colsStr); err == nil {
			return cols
		}
	}

	if t, ok := w.(interface{ Fd() uintptr }); ok {
		fd := t.Fd()
		var dimensions [4]uint16

		if _, _, err := syscall.Syscall6(
			syscall.SYS_IOCTL,
			uintptr(fd), //nolint: unconvert
			uintptr(syscall.TIOCGWINSZ),
			uintptr(unsafe.Pointer(&dimensions)), //nolint: gas
			0, 0, 0,
		); err == 0 {
			if dimensions[1] == 0 {
				return 80
			}
			return int(dimensions[1])
		}
	}
	return 80
}
