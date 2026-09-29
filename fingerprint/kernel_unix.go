// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build unix

package fingerprint

import (
	"bytes"
	"runtime"

	"golang.org/x/sys/unix"
)

func cstrToStr(b []byte) string {
	return string(b[:bytes.IndexByte(b, 0)])
}

// getKernelReleaseVersion returns the kernel release and version.
func getKernelReleaseVersion() (string, string) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return "", ""
	}

	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return "", ""
	}

	return cstrToStr(u.Release[:]), cstrToStr(u.Version[:])
}
