// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"testing"

	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/image-spec/schemes"
)

func TestCopy(t *testing.T) {
	ctx := t.Context()
	a := imagespec.NewAccessor()
	src, dst := t.TempDir(), t.TempDir()
	img := imagespec.NewImage(
		imagespec.WithPlatform(ocispec.Platform{OS: imagespec.PlatformFirecracker, Architecture: imagespec.ArchitectureIntel}),
		imagespec.WithKernel(imagespec.NewStaticFile("kernel", []byte("kernel"))),
	)
	saved, err := a.Save(ctx, &imagespec.Location{Scheme: schemes.OCILayout, Path: src}, img)
	require.NoError(t, err)

	require.NoError(t, (&CopyCmd{Source: "oci-layout://" + src, Destination: "oci-layout://" + dst, Insecure: "none"}).Run(ctx))

	copied, err := a.LoadAll(ctx, imagespec.Resolved{Location: imagespec.Location{Scheme: schemes.OCILayout, Path: dst}}, platforms.All)
	require.NoError(t, err)
	for _, img := range copied {
		require.NoError(t, img.Close())
	}
	require.Equal(t, saved.Descriptor.Digest, copied[0].Descriptor.Digest)
}
