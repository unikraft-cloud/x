// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package stdio provides a simple struct to bundle the standard streams
// together.
package stdio

import "io"

// Stdio bundles the standard streams so they can be passed around
// as a single unified object.
type Stdio struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
}
