// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imageref_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"unikraft.com/x/image-spec/imageref"
)

// The default domain and prefix complete what an identifier leaves implicit.
func TestParseDefaults(t *testing.T) {
	opts := []imageref.ParseOpt{
		imageref.WithDefaultDomain("unikraft.io"),
		// Deliberately without a trailing slash: the option supplies it.
		imageref.WithDefaultPrefix("official"),
	}

	for _, tt := range []struct{ name, in, want string }{
		{"a bare name gets the domain and prefix", "nginx", "unikraft.io/official/nginx"},
		{"a tag survives", "nginx:v1", "unikraft.io/official/nginx:v1"},
		{"the default domain spelled out still gets the prefix", "unikraft.io/nginx", "unikraft.io/official/nginx"},
		{"a namespaced name keeps its namespace", "myuser/app", "unikraft.io/myuser/app"},
		{"a third-party domain gets no prefix", "example.org/nginx", "example.org/nginx"},
		{"localhost is a domain, not a namespace", "localhost/nginx", "localhost/nginx"},

		// Docker Hub's implicit namespace is "library/", not the caller's.
		{"docker hub gets library, not the caller's prefix", "docker.io/nginx", "docker.io/library/nginx"},
		{"the legacy docker index is canonicalized", "index.docker.io/nginx", "docker.io/library/nginx"},
		{"a namespaced docker hub name is untouched", "docker.io/myuser/app", "docker.io/myuser/app"},

		{"index.unikraft.io stays distinct", "index.unikraft.io/me/app", "index.unikraft.io/me/app"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := imageref.Parse(tt.in, opts...)
			require.NoError(t, err)
			require.Equal(t, tt.want, ref.Named().String())
			require.Equal(t, tt.want, ref.String())
		})
	}
}

// With no options this has to behave exactly like the reference package's own
// ParseNormalizedNamed, since that is what every caller that does not care
// about a registry of its own expects.
func TestParseMatchesUpstreamNormalizationByDefault(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"nginx", "docker.io/library/nginx"},
		{"nginx:v1", "docker.io/library/nginx:v1"},
		{"myuser/app", "docker.io/myuser/app"},
		{"docker.io/nginx", "docker.io/library/nginx"},
		{"index.docker.io/nginx", "docker.io/library/nginx"},
		{"example.org:5000/nginx", "example.org:5000/nginx"},
	} {
		ref, err := imageref.Parse(tt.in)
		require.NoError(t, err, "input %q", tt.in)
		require.Equal(t, tt.want, ref.Named().String(), "input %q", tt.in)
	}
}

// A bare content identifier names no repository. The reference package rejects
// it.
func TestParseRejectsBareIdentifier(t *testing.T) {
	const identifier = "43d3d758e6fba7d4734ac142cfdbf8aa786fcbbfd828017eecaadc5140a4b190"

	_, err := imageref.Parse(identifier, imageref.WithDefaultDomain("unikraft.io"))
	require.ErrorIs(t, err, imageref.ErrInvalidReference)
	require.ErrorContains(t, err, "cannot specify 64-byte hexadecimal strings")

	// The guard is anchored, so it must not swallow an identifier-shaped name
	// that carries a tag, nor a shorter or longer hex string.
	for _, in := range []string{identifier + ":v1", identifier[:63], identifier + "a"} {
		ref, err := imageref.Parse(in, imageref.WithDefaultDomain("unikraft.io"), imageref.WithDefaultPrefix("official/"))
		require.NoError(t, err, "input %q", in)
		require.True(t, strings.HasPrefix(ref.String(), "unikraft.io/official/"), "input %q -> %s", in, ref)
	}
}

func TestParseOptionsIgnoreEmptyValues(t *testing.T) {
	// A caller computing a domain or prefix from config must not be able to
	// produce "/official/nginx" or "unikraft.io/nginx" by passing "".
	ref, err := imageref.Parse("nginx",
		imageref.WithDefaultDomain(""), imageref.WithDefaultPrefix(""))
	require.NoError(t, err)
	require.Equal(t, "docker.io/library/nginx", ref.String())
}
