// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package reference_test

import (
	"strings"
	"testing"

	distref "github.com/distribution/reference"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/image-spec/reference"
)

const testDigest = "sha256:43d3d758e6fba7d4734ac142cfdbf8aa786fcbbfd828017eecaadc5140a4b190"

func TestWithDefaultTag(t *testing.T) {
	ref, err := reference.Parse("nginx")
	require.NoError(t, err)
	require.Empty(t, ref.Tag())

	tagged := ref.WithDefaultTag()
	require.Equal(t, "latest", tagged.Tag())
	require.Equal(t, "unikraft.io/official/nginx:latest", tagged.String())

	require.Equal(t, tagged, tagged.WithDefaultTag())
	require.Equal(t, ref, tagged.WithoutDefaultTag())
}

func TestWithoutDefaultTag(t *testing.T) {
	for _, tt := range []struct{ name, in, want string }{
		{"latest is removed", "index.unikraft.io/me/app:latest", "index.unikraft.io/me/app"},
		{"another tag is kept", "index.unikraft.io/me/app:v1", "index.unikraft.io/me/app:v1"},
		{"no tag is a no-op", "index.unikraft.io/me/app@" + testDigest, "index.unikraft.io/me/app@" + testDigest},
		{
			"a digest survives removing latest",
			"index.unikraft.io/me/app:latest@" + testDigest,
			"index.unikraft.io/me/app@" + testDigest,
		},
		{"http is untouched", "https+oci://example.org/me/app/latest", "https+oci://example.org/me/app/latest"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := reference.Parse(tt.in)
			require.NoError(t, err)

			trimmed := ref.WithoutDefaultTag()
			require.Equal(t, tt.want, trimmed.String())

			require.Equal(t, tt.in, ref.String())
		})
	}
}

func TestFormat(t *testing.T) {
	opts := reference.FormatOpts{}

	for _, tt := range []struct{ name, in, want, wantShort string }{
		{
			name: "default domain and prefix are elided",
			in:   "unikraft.io/official/nginx:v1", want: "nginx:v1", wantShort: "nginx:v1",
		},
		{
			name: "a nested name keeps the prefix",
			in:   "unikraft.io/official/utils/volimport:1.0",
			want: "official/utils/volimport:1.0", wantShort: "official/utils/volimport:1.0",
		},
		{
			name: "a namespaced name keeps its namespace",
			in:   "unikraft.io/myuser/app:v1", want: "myuser/app:v1", wantShort: "myuser/app:v1",
		},
		{
			name: "another domain is kept",
			in:   "index.unikraft.io/me/app:v1", want: "index.unikraft.io/me/app:v1", wantShort: "index.unikraft.io/me/app:v1",
		},
		{
			name: "the digest is elided only in short form",
			in:   "unikraft.io/official/nginx@" + testDigest,
			want: "nginx@" + testDigest, wantShort: "nginx",
		},
		{
			name: "a tag and a digest are both shown",
			in:   "unikraft.io/official/nginx:v1@" + testDigest,
			want: "nginx:v1@" + testDigest, wantShort: "nginx:v1",
		},
		{
			name:      "http renders as its URI",
			in:        "https+oci://example.org/me/app/@" + testDigest,
			want:      "https+oci://example.org/me/app/@" + testDigest,
			wantShort: "https+oci://example.org/me/app/@" + testDigest,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := reference.Parse(tt.in)
			require.NoError(t, err)

			require.Equal(t, tt.want, ref.Format(opts))

			short := opts
			short.OmitDigest = true
			require.Equal(t, tt.wantShort, ref.Format(short))

			back, err := reference.Parse(tt.want)
			require.NoError(t, err)
			require.Equal(t, ref, back, "Format is not round-tripping")
		})
	}

	require.Empty(t, reference.Reference{}.Format(opts))
}

func TestMatches(t *testing.T) {
	parse := func(t *testing.T, s string) reference.Reference {
		t.Helper()
		ref, err := reference.Parse(s)
		require.NoError(t, err)
		return ref
	}

	for _, tt := range []struct {
		name    string
		subject string
		pattern string
		want    bool
	}{
		{"same uri", "http+oci://cdn.example.com/me/app/latest", "http+oci://cdn.example.com/me/app/latest", true},
		{"transport differs", "http+oci://cdn.example.com/me/app/latest", "https+oci://cdn.example.com/me/app/latest", false},
		{"host differs", "http+oci://cdn.example.com/me/app/latest", "http+oci://other.example.com/me/app/latest", false},
		{"a reference does not match a uri", "http+oci://cdn.example.com/me/app/latest", "cdn.example.com/me/app:latest", false},
		{"a uri does not match a reference", "cdn.example.com/me/app:latest", "http+oci://cdn.example.com/me/app/latest", false},
		{"exact registry reference", "unikraft.io/official/nginx:latest", "unikraft.io/official/nginx:latest", true},
		{"familiar short form", "unikraft.io/official/nginx:latest", "nginx", true},
		{"path alone", "unikraft.io/official/nginx:latest", "official/nginx", true},
		{"an untagged pattern matches any tag", "unikraft.io/official/nginx:v1", "unikraft.io/official/nginx", true},
		{"tag mismatch", "unikraft.io/official/nginx:v1", "unikraft.io/official/nginx:v2", false},
		{"repository mismatch", "unikraft.io/official/nginx:v1", "unikraft.io/official/other:v1", false},
		{"a host-shaped path is not a domain", "unikraft.io/example.com/foo:latest", "example.com/foo", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, parse(t, tt.subject).Matches(parse(t, tt.pattern)))
		})
	}

	subject := parse(t, "unikraft.io/official/nginx@"+testDigest)
	require.True(t, subject.Matches(parse(t, "unikraft.io/official/nginx@"+testDigest)))
	require.False(t, subject.Matches(parse(t, "unikraft.io/official/nginx@sha256:"+
		"0000000000000000000000000000000000000000000000000000000000000000")))

	require.False(t, reference.Reference{}.Matches(subject))
	require.False(t, subject.Matches(reference.Reference{}))
}

func TestWithTagAndWithDigest(t *testing.T) {
	for _, tt := range []struct {
		in         string
		wantString string
	}{
		{"unikraft.io/official/nginx:v1", "unikraft.io/official/nginx:v3@" + testDigest},
		{"https+oci://example.org/me/app/latest", "https+oci://example.org/me/app/@" + testDigest},
	} {
		t.Run(tt.in, func(t *testing.T) {
			ref, err := reference.Parse(tt.in)
			require.NoError(t, err)

			tagged, err := ref.WithTag("v2")
			require.NoError(t, err)
			require.Equal(t, "v2", tagged.Tag())
			require.Empty(t, tagged.Digest())

			digested, err := tagged.WithDigest(testDigest)
			require.NoError(t, err)
			require.Equal(t, testDigest, digested.Digest().String())
			require.Equal(t, "v2", digested.Tag(), "a digest keeps the tag")

			retagged, err := digested.WithTag("v3")
			require.NoError(t, err)
			require.Equal(t, "v3", retagged.Tag())
			require.Equal(t, testDigest, retagged.Digest().String(), "a tag keeps the digest")
			require.Equal(t, tt.wantString, retagged.String())

			require.Equal(t, tt.in, ref.String())
		})
	}

	ref, err := reference.Parse("nginx")
	require.NoError(t, err)
	_, err = ref.WithTag("not a tag")
	require.ErrorIs(t, err, reference.ErrInvalidReference)
	_, err = ref.WithDigest("sha256:nothex")
	require.ErrorIs(t, err, reference.ErrInvalidReference)
}

func TestHTTPWithTagAndDigest(t *testing.T) {
	ref, err := reference.Parse("https+oci://example.org/me/app/latest")
	require.NoError(t, err)
	ref, err = ref.WithDigest(testDigest)
	require.NoError(t, err)

	require.Equal(t, "https://example.org/me/app/@"+testDigest, ref.AsURL().String())
	require.Equal(t, "https+oci://example.org/me/app/@"+testDigest, ref.Format(reference.FormatOpts{}))
	require.Equal(t, "https+oci://example.org/me/app/latest", ref.Format(reference.FormatOpts{OmitDigest: true}))

	for _, pattern := range []string{
		"https+oci://example.org/me/app/latest",
		"https+oci://example.org/me/app/@" + testDigest,
	} {
		p, err := reference.Parse(pattern)
		require.NoError(t, err)
		require.True(t, ref.Matches(p), "pattern %q", pattern)
	}

	back, err := reference.Parse(ref.String())
	require.NoError(t, err)
	require.Empty(t, back.Tag(), "only the digest goes over the wire")
	require.Equal(t, ref.Digest(), back.Digest())
}

func TestWithoutTagAndDigest(t *testing.T) {
	ref, err := reference.Parse("unikraft.io/official/nginx:v1@" + testDigest)
	require.NoError(t, err)
	require.Equal(t, "unikraft.io/official/nginx@"+testDigest, ref.WithoutTag().String())
	require.Equal(t, "unikraft.io/official/nginx:v1", ref.WithoutDigest().String())
	require.Equal(t, "unikraft.io/official/nginx", ref.WithoutTag().WithoutDigest().String())

	http, err := reference.Parse("https+oci://example.org/me/app/latest")
	require.NoError(t, err)
	http, err = http.WithDigest(testDigest)
	require.NoError(t, err)
	require.Equal(t, "https+oci://example.org/me/app/@"+testDigest, http.WithoutTag().String())
	require.Equal(t, "https+oci://example.org/me/app/latest", http.WithoutDigest().String())

	trimmed := http.WithoutTag().WithoutDigest()
	require.Empty(t, trimmed.Tag())
	require.Equal(t, "https+oci://example.org/me/app/latest", trimmed.String(), "like OCI, no tag means latest")
	require.Equal(t, "https://example.org/me/app/latest", trimmed.AsURL().String())
	retagged, err := trimmed.WithTag("v2")
	require.NoError(t, err)
	require.Equal(t, "https+oci://example.org/me/app/v2", retagged.String())

	require.True(t, reference.Reference{}.WithoutTag().WithoutDigest().IsZero())
}

func TestReferenceIsComparable(t *testing.T) {
	a, err := reference.Parse("nginx:latest")
	require.NoError(t, err)
	b, err := reference.Parse("nginx:latest")
	require.NoError(t, err)

	// Compared with == via a variable so testifylint does not rewrite it.
	sameImage := a == b
	require.True(t, sameImage, "two references to the same image must be equal")
	require.True(t, a.Equal(b))

	seen := map[reference.Reference]struct{}{a: {}}
	_, ok := seen[b]
	require.True(t, ok, "an equal reference must find its own map entry")

	c, err := reference.Parse("nginx:v1")
	require.NoError(t, err)
	differentImage := a == c
	require.False(t, differentImage, "references to different images must not be equal")
}

func TestZeroReference(t *testing.T) {
	var ref reference.Reference

	require.True(t, ref.IsZero())
	require.Empty(t, ref.Scheme())
	require.Empty(t, ref.Domain())
	require.Empty(t, ref.Path())
	require.Empty(t, ref.Tag())
	require.Empty(t, ref.Digest())
	require.Empty(t, ref.Name())
	require.Nil(t, ref.AsNamed())
	require.Nil(t, ref.AsURL())
	require.Empty(t, ref.String())
	require.Empty(t, ref.Format(reference.FormatOpts{}))
	require.Equal(t, ref, ref.WithDefaultTag())
	require.Equal(t, ref, ref.WithoutDefaultTag())

	_, err := ref.WithTag("v1")
	require.ErrorIs(t, err, reference.ErrInvalidReference)
	_, err = ref.WithDigest(testDigest)
	require.ErrorIs(t, err, reference.ErrInvalidReference)
}

func TestWithDomain(t *testing.T) {
	ref, err := reference.Parse("index.unikraft.io/official/nginx:v1")
	require.NoError(t, err)

	moved, err := ref.WithDomain("unikraft.io")
	require.NoError(t, err)
	require.Equal(t, "unikraft.io/official/nginx:v1", moved.String())
	require.Equal(t, "index.unikraft.io/official/nginx:v1", ref.String(), "the receiver is a value")

	canonical, err := reference.Parse("unikraft.io/official/nginx:v1")
	require.NoError(t, err)
	require.Equal(t, canonical, moved)

	http, err := reference.Parse("https+oci://cdn.example.com/me/app/latest")
	require.NoError(t, err)
	moved, err = http.WithDomain("OTHER.example.com:443")
	require.NoError(t, err)
	require.Equal(t, "https+oci://other.example.com/me/app/latest", moved.String())

	_, err = ref.WithDomain("NOT A HOST")
	require.ErrorIs(t, err, reference.ErrInvalidReference)
	_, err = reference.Reference{}.WithDomain("unikraft.io")
	require.ErrorIs(t, err, reference.ErrInvalidReference)
}

func TestStringIsAFixedPoint(t *testing.T) {
	for _, in := range []string{
		"nginx",
		"nginx:v1",
		"myuser/app:v1",
		"docker.io/library/nginx:latest",
		"index.unikraft.io/me/app@" + testDigest,
		"index.unikraft.io/me/app:v1@" + testDigest,
		"example.org:5000/me/app:v1",
		"localhost/me/app:v1",
		"localhost:5000/me/app:v1",
		"127.0.0.1:5000/me/app:v1",
		"me/app:v1.2.3-alpha_4",

		"http+oci://cdn.example.com/me/app/latest",
		"https+oci://cdn.example.com/me/app/@" + testDigest,
		"http+oci://cdn.example.com:8080/me/app/latest",
		"http+oci://[::1]:8080/me/app/latest",
		"http+oci://127.0.0.1:8080/me/app/latest",
		"https+oci://a.b.c.example.com/one/two/three/four/latest",
		"http+oci://cdn.example.com/me/app/v1.2.3-alpha_4",
	} {
		t.Run(in, func(t *testing.T) {
			ref, err := reference.Parse(in)
			require.NoError(t, err)

			once := ref.String()
			again, err := reference.Parse(once)
			require.NoError(t, err)
			require.Equal(t, ref, again, "%q -> %q did not re-parse to an equal reference", in, once)
			require.Equal(t, once, again.String(), "String() is not a fixed point")

			if ref.Scheme().IsHTTP() {
				require.Nil(t, ref.AsNamed())
				transport := strings.TrimSuffix(string(ref.Scheme()), "+oci")
				require.Equal(t, transport+strings.TrimPrefix(ref.String(), string(ref.Scheme())),
					ref.AsURL().String())
			} else {
				require.NotNil(t, ref.AsNamed())
				require.Nil(t, ref.AsURL())
			}
		})
	}
}

func TestFormatDoesNotRenderADifferentImage(t *testing.T) {
	opts := reference.FormatOpts{}

	for _, tt := range []struct{ name, want string }{
		{"unikraft.io/official/nginx", "nginx"},
		{"unikraft.io/me/app", "me/app"},
		{"unikraft.io/official/utils/volimport", "official/utils/volimport"},
		{"unikraft.io/nginx", "unikraft.io/nginx"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			named, err := distref.WithName(tt.name)
			require.NoError(t, err)
			ref, err := reference.FromNamed(named)
			require.NoError(t, err)

			require.Equal(t, tt.want, ref.Format(opts))
		})
	}
}

func TestWithDomainRejectsANonHost(t *testing.T) {
	ref, err := reference.Parse("nginx")
	require.NoError(t, err)

	_, err = ref.WithDomain("myhost")
	require.ErrorIs(t, err, reference.ErrInvalidReference)

	_, err = ref.WithDomain("example.com/extra")
	require.ErrorIs(t, err, reference.ErrInvalidReference)

	http, err := reference.Parse("https+oci://cdn.example.com/me/app/latest")
	require.NoError(t, err)
	_, err = http.WithDomain("example.com/extra")
	require.ErrorIs(t, err, reference.ErrInvalidReference)
}

func TestLegacyDockerDomain(t *testing.T) {
	for _, tt := range []struct {
		name   string
		in     string
		opts   []reference.ParseOpt
		domain string
		want   string
	}{
		{name: "oci name", in: "index.docker.io/nginx", want: "docker.io/library/nginx"},
		{name: "oci namespaced name", in: "index.docker.io/me/app:v1", want: "docker.io/me/app:v1"},
		{
			name: "oci default domain",
			in:   "nginx",
			opts: []reference.ParseOpt{reference.WithDefaultDomain("index.docker.io")},
			want: "docker.io/library/nginx",
		},
		{name: "oci WithDomain", in: "unikraft.io/me/app:v1", domain: "index.docker.io", want: "docker.io/me/app:v1"},
		{name: "http host", in: "https+oci://index.docker.io/me/app/latest", want: "https+oci://index.docker.io/me/app/latest"},
		{
			name:   "http WithDomain",
			in:     "https+oci://cdn.example.com/me/app/latest",
			domain: "index.docker.io",
			want:   "https+oci://index.docker.io/me/app/latest",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := reference.Parse(tt.in, tt.opts...)
			require.NoError(t, err)
			if tt.domain != "" {
				ref, err = ref.WithDomain(tt.domain)
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, ref.String())

			back, err := reference.Parse(ref.String(), tt.opts...)
			require.NoError(t, err)
			require.Equal(t, ref, back)
		})
	}
}
