// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package io

import (
	"io"
	"os"
)

// IsStdin reports whether r is the process's own standard input.
func IsStdin(r io.Reader) bool { return isStd(r, os.Stdin) }

// IsStdout reports whether the writer ultimately targets the process's own
// standard output, transparently peeling off known wrappers via Unwrap.
func IsStdout(w io.Writer) bool { return isStd(Unwrap(w), os.Stdout) }

func isStd(v any, std *os.File) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	si, err := std.Stat()
	return err == nil && os.SameFile(fi, si)
}
