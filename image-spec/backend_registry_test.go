// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/image-spec/reference"
)

func TestRegistryResolvedReference(t *testing.T) {
	dgst := digest.FromString("index")
	other := digest.FromString("other")

	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{name: "a tag is kept", src: "example.com/app:v1", want: "example.com/app:v1@" + dgst.String()},
		{name: "no tag is latest", src: "example.com/app", want: "example.com/app:latest@" + dgst.String()},
		{name: "a digest stays untagged", src: "example.com/app@" + other.String(), want: "example.com/app@" + dgst.String()},
		{name: "a tag and digest keep the tag", src: "example.com/app:v1@" + other.String(), want: "example.com/app:v1@" + dgst.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			named, err := reference.ParseNormalizedNamed(tc.src)
			require.NoError(t, err)

			got, err := registryResolvedReference(named, ocispec.Descriptor{Digest: dgst})
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.String())
		})
	}
}
