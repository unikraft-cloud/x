// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package fingerprint

import (
	"os"
	"path/filepath"
	"strings"
)

// getDmi returns the DMI system vendor and product name.
func getDmi() (string, string) {
	const dir = "/sys/class/dmi/id"
	return readDmi(dir, "sys_vendor"), readDmi(dir, "product_name")
}

// readDmi replaces invalid UTF-8, since firmware strings are raw bytes and
// oidext refuses to encode them.
func readDmi(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.ToValidUTF8(strings.TrimSpace(string(b)), "\uFFFD")
}
