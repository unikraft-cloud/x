// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
)

func TestPlatforms(t *testing.T) {
	m := Platforms()
	for _, os := range []string{PlatformKraftcloud, PlatformFirecracker, PlatformQemu, ""} {
		for _, arch := range []string{"x86_64", "amd64", "arm64", "aarch64"} {
			assert.True(t, m.Match(ocispec.Platform{OS: os, Architecture: arch}), "%s/%s", os, arch)
		}
		assert.False(t, m.Match(ocispec.Platform{OS: os, Architecture: "riscv64"}), os)
	}
	for _, p := range []ocispec.Platform{
		{OS: "unknown", Architecture: "unknown"},
		{OS: "linux", Architecture: "amd64"},
		{OS: "windows", Architecture: "amd64"},
	} {
		assert.False(t, m.Match(p), "%s/%s", p.OS, p.Architecture)
	}

	assert.True(t, Platforms(RuntimeKindUnikraft).Match(ocispec.Platform{OS: PlatformQemu, Architecture: "arm64"}))

	ordered := []ocispec.Platform{
		{OS: PlatformKraftcloud, Architecture: "amd64"},
		{OS: PlatformKraftcloud, Architecture: "arm64"},
		{OS: PlatformFirecracker, Architecture: "x86_64"},
		{OS: PlatformQemu, Architecture: "aarch64"},
		{OS: "", Architecture: "x86_64"},
		{OS: "linux", Architecture: "amd64"},
	}
	for i := range ordered {
		for j := range ordered {
			assert.Equal(t, i < j, m.Less(ordered[i], ordered[j]), "%v < %v", ordered[i], ordered[j])
		}
	}
}
