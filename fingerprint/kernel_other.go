// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !unix && !js && !windows

package fingerprint

// getKernelReleaseVersion returns the kernel release and version.
func getKernelReleaseVersion() (string, string) {
	return "", ""
}
