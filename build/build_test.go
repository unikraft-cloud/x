// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"unikraft.com/x/kraftfile"
)

func TestDefaultRootfsFormatKernellessNoErofsFeatures(t *testing.T) {
	opts := BuildOpts{Platform: []ocispec.Platform{
		{Architecture: "x86_64", OS: "fc", OSFeatures: []string{"CONFIG_OTHER=y"}},
	}}
	require.Equal(t, kraftfile.FsTypeErofs, defaultRootfsFormat(opts))
}

func TestDefaultRootfsFormatMixedSupport(t *testing.T) {
	opts := BuildOpts{Runtime: "rt", Platform: []ocispec.Platform{
		{Architecture: "x86_64", OS: "fc", OSFeatures: []string{"CONFIG_LIBUKFS_EROFS=y"}},
		{Architecture: "arm64", OS: "fc", OSFeatures: []string{"CONFIG_OTHER=y"}},
	}}
	require.Equal(t, kraftfile.FsTypeCpio, defaultRootfsFormat(opts))
}

func TestDefaultRootfsFormatMixedErofsKeys(t *testing.T) {
	opts := BuildOpts{Runtime: "rt", Platform: []ocispec.Platform{
		{Architecture: "x86_64", OS: "fc", OSFeatures: []string{"CONFIG_LIBUKFS_EROFS=y"}},
		{Architecture: "arm64", OS: "fc", OSFeatures: []string{"CONFIG_EROFS_FS=y"}},
	}}
	require.Equal(t, kraftfile.FsTypeErofs, defaultRootfsFormat(opts))
}

func TestFilterArchWithRuntimeSetsMatcher(t *testing.T) {
	opts := BuildOpts{Runtime: "unikraft.io/official/base-compat"}
	require.NoError(t, opts.FilterArch("arm64"))
	require.Empty(t, opts.Platform)
	require.NotNil(t, opts.PlatformMatcher)
	require.True(t, opts.PlatformMatcher.Match(ocispec.Platform{OS: "fc", Architecture: "arm64"}))
	require.False(t, opts.PlatformMatcher.Match(ocispec.Platform{OS: "fc", Architecture: "x86_64"}))
}

func TestFilterArchDeclaredTargets(t *testing.T) {
	opts := BuildOpts{
		Runtime: "unikraft.io/official/base-compat",
		Platform: []ocispec.Platform{
			{OS: "fc", Architecture: "x86_64"},
			{OS: "fc", Architecture: "arm64"},
		},
	}
	require.NoError(t, opts.FilterArch("arm64"))
	require.Nil(t, opts.PlatformMatcher)
	require.Equal(t, []ocispec.Platform{{OS: "fc", Architecture: "arm64"}}, opts.Platform)
}

func TestFilterArchDeclaredTargetsAlias(t *testing.T) {
	opts := BuildOpts{
		Platform: []ocispec.Platform{
			{OS: "fc", Architecture: "x86_64"},
			{OS: "fc", Architecture: "arm64"},
		},
	}
	require.NoError(t, opts.FilterArch("amd64"))
	require.Equal(t, []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}}, opts.Platform)
}

func TestFilterArchNoTargetsAlias(t *testing.T) {
	var opts BuildOpts
	require.NoError(t, opts.FilterArch("aarch64", "arm64"))
	require.Equal(t, []ocispec.Platform{{OS: "kraftcloud", Architecture: "arm64"}}, opts.Platform)
}

func TestFilterArchUnsupported(t *testing.T) {
	var opts BuildOpts
	require.ErrorContains(t, opts.FilterArch("riscv64"), `unsupported architecture "riscv64"`)
}
