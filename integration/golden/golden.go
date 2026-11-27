// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package golden checks the output of a test against a golden file, which
// is in testdata and named after the test. Run the test with -update to
// write the file.
package golden

import (
	"strings"
	"testing"

	"gotest.tools/v3/golden"
)

// Gild runs callback for each of args, and checks their outputs, one after
// the other, against the golden file of the test. Only use it for output
// that is the same on every run.
func Gild[Arg any](t *testing.T, callback func(*testing.T, Arg) string, args ...Arg) {
	t.Helper()

	var output strings.Builder
	for i, arg := range args {
		if i > 0 {
			output.WriteString("\n")
		}
		output.WriteString(callback(t, arg))
	}
	golden.Assert(t, output.String(), t.Name())
}
