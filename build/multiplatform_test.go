// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/moby/buildkit/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/image-spec/schemes"
	"unikraft.com/x/kraftfile"

	"unikraft.com/x/build"
)

// TestRootfsAcrossDaemons builds each platform of a rootfs on its own
// buildkitd, the arm64 one emulated on an x86_64 host, and checks that the
// manifests are those of a build on one daemon, whether one Save indexes them
// or each platform is pushed on its own.
func TestRootfsAcrossDaemons(t *testing.T) {
	native, emulated := os.Getenv("BUILDKIT_HOST"), os.Getenv("BUILDKIT_HOST_ARM64")
	if native == "" || emulated == "" {
		t.Skip("BUILDKIT_HOST and BUILDKIT_HOST_ARM64 must name two buildkitds")
	}
	kcIntel := ocispec.Platform{OS: "kraftcloud", Architecture: "x86_64"}
	kcArm := ocispec.Platform{OS: "kraftcloud", Architecture: "arm64"}

	// RUN needs emulation for arm64, and the epoch dates its output alike on
	// every daemon.
	dockerfile := filepath.Join(t.TempDir(), "Dockerfile")
	require.NoError(t, os.WriteFile(dockerfile, []byte(`
FROM busybox AS base
RUN uname -m > /arch

FROM scratch
COPY --from=base /arch /arch
`), 0o644))

	rootfs := func(addr string, ps ...ocispec.Platform) []*imagespec.Image {
		t.Helper()
		c, err := client.New(t.Context(), addr)
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })

		b := build.Builder{Buildkit: func(context.Context) (*client.Client, func(), error) {
			return c, nil, nil
		}}
		imgs, err := b.BuildRootfs(t.Context(), build.BuildOpts{
			Rootfs:   build.FSOpts{Path: dockerfile, Type: kraftfile.SourceTypeDockerfile},
			Platform: ps,
			BuildArg: []string{"SOURCE_DATE_EPOCH=0"},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			for _, img := range imgs {
				_ = img.Close()
			}
		})
		return imgs
	}

	// Each push has its own resolver, which skips content it pushed before to
	// any repository.
	host := startRegistry(t)
	push := func(ref string, imgs ...*imagespec.Image) {
		t.Helper()
		_, err := registryBuilder(alice).Save(t.Context(), &imagespec.Location{Scheme: schemes.OCI, Path: host + "/" + ref}, imgs...)
		require.NoError(t, err)
	}

	push("one", rootfs(native, kcIntel, kcArm)...)
	want := registryIndex(t, host, "one", "latest").Manifests
	require.Len(t, want, 2)

	split := slices.Concat(rootfs(native, kcIntel), rootfs(emulated, kcArm))
	push("split", split...)
	assert.Equal(t, want, registryIndex(t, host, "split", "latest").Manifests)

	// As a process per daemon would, push each platform on its own: an index
	// of the manifests these pushes left is the one above.
	var joined []ocispec.Descriptor
	for _, img := range split {
		tag := img.Image.Architecture
		push("joined:"+tag, img)
		joined = append(joined, registryIndex(t, host, "joined", tag).Manifests...)
	}
	assert.Equal(t, want, joined)
}
