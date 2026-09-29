// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build js

package fingerprint

import (
	"os"
	"runtime"

	"unikraft.com/x/ptr"
)

// New reports what a js host can know about itself.  The runtime exposes no
// machine characteristics, so only the build's own identity is filled in.
func New() (*Fingerprint, error) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = runtime.GOOS
	}

	return &Fingerprint{
		Hostname:  hostname,
		Os:        runtime.GOOS,
		Goarch:    runtime.GOARCH,
		Goos:      runtime.GOOS,
		GoVersion: ptr.NilIfZero(runtime.Version()),
	}, nil
}
