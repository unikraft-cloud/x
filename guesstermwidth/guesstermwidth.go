// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package guesstermwidth

import (
	"io"
	"os"
	"strconv"

	"github.com/charmbracelet/x/term"
)

// IsTTY checks if the given writer is a terminal.
func IsTTY(w io.Writer) bool {
	f, ok := w.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(f.Fd())
}

// GuessTermWidth guesses the terminal width based on the COLUMNS environment
// variable or by querying the terminal's window size.
func GuessTermWidth(w io.Writer) int {
	// check if COLUMNS env is set to comply with
	// http://pubs.opengroup.org/onlinepubs/009604499/basedefs/xbd_chap08.html
	if cols, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil {
		return cols
	}
	if f, ok := w.(interface{ Fd() uintptr }); ok {
		if width, _, err := term.GetSize(f.Fd()); err == nil && width > 0 {
			return width
		}
	}
	return 80
}
