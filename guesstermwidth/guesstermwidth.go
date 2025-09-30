// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build appengine || (!linux && !freebsd && !darwin && !dragonfly && !netbsd && !openbsd)
// +build appengine !linux,!freebsd,!darwin,!dragonfly,!netbsd,!openbsd

package guesstermwidth

import "io"

// GuessTermWidth returns a default terminal width of 80 characters since the
// environment does not support querying terminal dimensions.
func GuessTermWidth(w io.Writer) int {
	return 80
}
