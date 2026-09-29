// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package fingerprint

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// getKernelReleaseVersion returns the kernel release and version.
func getKernelReleaseVersion() (string, string) {
	major, minor, build := windows.RtlGetNtVersionNumbers()
	return fmt.Sprintf("%d.%d.%d", major, minor, build), ""
}
