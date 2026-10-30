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
	"unikraft.com/x/image-spec/schemes"
)

func TestParseDefaults(t *testing.T) {
	for _, tt := range []struct{ name, in, want string }{
		{"a bare name gets the domain and prefix", "nginx", "unikraft.io/official/nginx"},
		{"a tag survives", "nginx:v1", "unikraft.io/official/nginx:v1"},
		{"the default domain spelled out still gets the prefix", "unikraft.io/nginx", "unikraft.io/official/nginx"},
		{"a namespaced name keeps its namespace", "myuser/app", "unikraft.io/myuser/app"},
		{"a third-party domain gets no prefix", "example.org/nginx", "example.org/nginx"},
		{"localhost is a domain, not a namespace", "localhost/nginx", "localhost/nginx"},

		{"docker hub gets library, not official", "docker.io/nginx", "docker.io/library/nginx"},
		{"the legacy docker index is canonicalized", "index.docker.io/nginx", "docker.io/library/nginx"},
		{"a namespaced docker hub name is untouched", "docker.io/myuser/app", "docker.io/myuser/app"},

		{"index.unikraft.io stays distinct", "index.unikraft.io/me/app", "index.unikraft.io/me/app"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := reference.Parse(tt.in)
			require.NoError(t, err)
			require.Equal(t, tt.want, ref.AsNamed().String())
			require.Equal(t, tt.want, ref.String())
		})
	}
}

func TestParseMatchesUpstreamOnDockerHub(t *testing.T) {
	opt := reference.WithDefaultDomain("docker.io")
	for _, tt := range []struct{ in, want string }{
		{"nginx", "docker.io/library/nginx"},
		{"nginx:v1", "docker.io/library/nginx:v1"},
		{"myuser/app", "docker.io/myuser/app"},
		{"docker.io/nginx", "docker.io/library/nginx"},
		{"index.docker.io/nginx", "docker.io/library/nginx"},
		{"example.org:5000/nginx", "example.org:5000/nginx"},
	} {
		ref, err := reference.Parse(tt.in, opt)
		require.NoError(t, err, "input %q", tt.in)
		require.Equal(t, tt.want, ref.AsNamed().String(), "input %q", tt.in)

		named, err := reference.ParseNormalizedNamed(tt.in, opt)
		require.NoError(t, err, "input %q", tt.in)
		upstream, err := distref.ParseNormalizedNamed(tt.in)
		require.NoError(t, err, "input %q", tt.in)
		require.Equal(t, upstream.String(), named.String(), "input %q", tt.in)
	}
}

func TestParseNormalizedNamed(t *testing.T) {
	named, err := reference.ParseNormalizedNamed("nginx:v1")
	require.NoError(t, err)
	require.Equal(t, "unikraft.io/official/nginx:v1", named.String())

	_, err = reference.ParseNormalizedNamed("oci://nginx")
	require.ErrorIs(t, err, reference.ErrInvalidReference)
	_, err = reference.ParseNormalizedNamed("Nginx")
	require.ErrorIs(t, err, reference.ErrInvalidReference)
}

func TestParseRejectsBareIdentifier(t *testing.T) {
	const identifier = "43d3d758e6fba7d4734ac142cfdbf8aa786fcbbfd828017eecaadc5140a4b190"

	_, err := reference.Parse(identifier)
	require.ErrorIs(t, err, reference.ErrInvalidReference)
	require.ErrorContains(t, err, "cannot specify 64-byte hexadecimal strings")

	for _, in := range []string{identifier + ":v1", identifier[:63], identifier + "a"} {
		ref, err := reference.Parse(in)
		require.NoError(t, err, "input %q", in)
		require.True(t, strings.HasPrefix(ref.String(), "unikraft.io/official/"), "input %q -> %s", in, ref)
	}
}

func TestParseOptionsIgnoreEmptyValues(t *testing.T) {
	ref, err := reference.Parse("nginx", reference.WithDefaultDomain(""))
	require.NoError(t, err)
	require.Equal(t, "unikraft.io/official/nginx", ref.String())
}

func TestParseHTTP(t *testing.T) {
	tests := []struct {
		name    string
		uri     string
		scheme  schemes.Scheme
		domain  string
		path    string
		url     string
		tag     string
		digest  string
		wantErr string
	}{
		{
			name:   "tag",
			uri:    "https+oci://username.unikraftcdn.com/some/path/to/layout/latest",
			scheme: schemes.HTTPSOCI,
			domain: "username.unikraftcdn.com",
			path:   "some/path/to/layout",
			url:    "https://username.unikraftcdn.com/some/path/to/layout/latest",
			tag:    "latest",
		},
		{
			name:   "digest",
			uri:    "https+oci://username.unikraftcdn.com/some/path/to/layout/@" + testDigest,
			scheme: schemes.HTTPSOCI,
			domain: "username.unikraftcdn.com",
			path:   "some/path/to/layout",
			url:    "https://username.unikraftcdn.com/some/path/to/layout/@" + testDigest,
			digest: testDigest,
		},
		{
			name:   "host with port",
			uri:    "http+oci://localhost:8001/root/helloworld/latest",
			scheme: schemes.HTTPOCI,
			domain: "localhost:8001",
			path:   "root/helloworld",
			url:    "http://localhost:8001/root/helloworld/latest",
			tag:    "latest",
		},
		{
			name:   "multi-label host",
			uri:    "http+oci://layouts.internal.example.com/test/registry/latest",
			scheme: schemes.HTTPOCI,
			domain: "layouts.internal.example.com",
			path:   "test/registry",
			url:    "http://layouts.internal.example.com/test/registry/latest",
			tag:    "latest",
		},
		{
			name:   "host without a dot keeps its default port",
			uri:    "http+oci://myhost:80/repo/name/latest",
			scheme: schemes.HTTPOCI,
			domain: "myhost:80",
			path:   "repo/name",
			url:    "http://myhost:80/repo/name/latest",
			tag:    "latest",
		},
		{
			name:   "localhost without a port",
			uri:    "http+oci://localhost/repo/name/latest",
			scheme: schemes.HTTPOCI,
			domain: "localhost",
			path:   "repo/name",
			url:    "http://localhost/repo/name/latest",
			tag:    "latest",
		},
		{
			name:    "host without a dot or port",
			uri:     "http+oci://myhost/repo/name/latest",
			wantErr: `host "myhost" is not a registry`,
		},
		{
			name:    "digest without an @ prefix",
			uri:     "https+oci://username.unikraftcdn.com/some/path/to/layout/" + testDigest,
			wantErr: "invalid tag format",
		},
		{
			name:    "missing repository name",
			uri:     "http+oci://localhost:8080/" + testDigest,
			wantErr: "missing a repository name",
		},
		{
			name:    "single repository segment",
			uri:     "https+oci://example.org/nginx/latest",
			wantErr: `repository "nginx" must be <namespace>/<name>`,
		},
		{
			name:    "terminating slash",
			uri:     "https+oci://example.org/root/nginx/",
			wantErr: "unexpected terminating '/'",
		},
		{
			name:    "no path at all",
			uri:     "https+oci://example.org",
			wantErr: "missing a repository and identifier",
		},
		{
			name:    "missing host",
			uri:     "http+oci://",
			wantErr: "missing host",
		},
		{
			name:    "port without a host",
			uri:     "http+oci://:8080/root/nginx/latest",
			wantErr: "missing host",
		},
		{
			name:    "embedded credentials",
			uri:     "http+oci://user:pw@example.org/root/nginx/latest",
			wantErr: "must not embed credentials",
		},
		{
			name:    "uppercase repository",
			uri:     "https+oci://example.org/Root/nginx/latest",
			wantErr: "invalid reference format",
		},
		{
			name:    "malformed digest",
			uri:     "http+oci://example.org/root/nginx/@sha256:nothex",
			wantErr: "invalid checksum digest",
		},
		{
			// url.Parse would decode the separator before the identifier is split off.
			name:    "escaped separator",
			uri:     "https+oci://example.org/root/nginx%2Flatest",
			wantErr: "must not percent-encode",
		},
		{
			name:    "escaped digest prefix",
			uri:     "https+oci://example.org/root/nginx/%40" + testDigest,
			wantErr: "must not percent-encode",
		},
		{
			name:    "empty path segment",
			uri:     "https+oci://example.org/root//nginx/latest",
			wantErr: "must not contain empty or relative path segments",
		},
		{
			name:    "relative path segment",
			uri:     "https+oci://example.org/root/../nginx/latest",
			wantErr: "must not contain empty or relative path segments",
		},
		{
			// A bare '?' sets ForceQuery, not RawQuery.
			name:    "bare query",
			uri:     "https+oci://example.org/root/nginx/latest?",
			wantErr: "must not have a query",
		},
		{
			name:    "query",
			uri:     "https+oci://example.org/root/nginx/latest?a=b",
			wantErr: "must not have a query",
		},
		{
			// url.Parse drops a bare '#' entirely.
			name:    "bare fragment",
			uri:     "https+oci://example.org/root/nginx/latest#",
			wantErr: "must not have a fragment",
		},
		{
			name:    "fragment",
			uri:     "https+oci://example.org/root/nginx/latest#frag",
			wantErr: "must not have a fragment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := reference.Parse(tt.uri)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				require.True(t, ref.IsZero(), "a rejected identifier must not yield a reference")
				return
			}
			require.NoError(t, err)

			require.Equal(t, tt.scheme, ref.Scheme())
			require.True(t, ref.Scheme().IsHTTP())
			require.Equal(t, tt.domain, ref.Domain())
			require.Equal(t, tt.path, ref.Path())

			require.Nil(t, ref.AsNamed())
			require.Equal(t, tt.url, ref.AsURL().String())
			require.Equal(t, tt.tag, ref.Tag())
			require.Equal(t, tt.digest, ref.Digest().String())

			require.Equal(t, tt.uri, ref.String())
			again, err := reference.Parse(ref.String())
			require.NoError(t, err)
			require.Equal(t, ref, again, "parsing String() must yield an equal reference")

			require.Equal(t, ref, ref.WithDefaultTag())
			require.Equal(t, ref, ref.WithoutDefaultTag())

			require.Equal(t, tt.uri, ref.Format(reference.FormatOpts{OmitDigest: true}))
		})
	}
}

func TestParseHTTPNormalizesHost(t *testing.T) {
	for _, tt := range []struct{ name, in, want string }{
		{"uppercase host", "http+oci://CDN.Example.COM/me/app/latest", "http+oci://cdn.example.com/me/app/latest"},
		{"default http port", "http+oci://cdn.example.com:80/me/app/latest", "http+oci://cdn.example.com/me/app/latest"},
		{"default https port", "https+oci://cdn.example.com:443/me/app/latest", "https+oci://cdn.example.com/me/app/latest"},
		{"non-default port is kept", "http+oci://cdn.example.com:8080/me/app/latest", "http+oci://cdn.example.com:8080/me/app/latest"},
		{"https default port on http is kept", "http+oci://cdn.example.com:443/me/app/latest", "http+oci://cdn.example.com:443/me/app/latest"},
		{"ipv6 literal keeps its brackets", "http+oci://[::1]:80/me/app/latest", "http+oci://[::1]/me/app/latest"},
		{"ipv6 literal with a port", "http+oci://[::1]:8080/me/app/latest", "http+oci://[::1]:8080/me/app/latest"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := reference.Parse(tt.in)
			require.NoError(t, err)
			require.Equal(t, tt.want, ref.String())

			again, err := reference.Parse(ref.String())
			require.NoError(t, err)
			require.Equal(t, ref, again)
		})
	}

	a, err := reference.Parse("http+oci://CDN.example.com:80/me/app/latest")
	require.NoError(t, err)
	b, err := reference.Parse("http+oci://cdn.example.com/me/app/latest")
	require.NoError(t, err)
	require.Equal(t, a, b)
	require.True(t, a.Matches(b))
}

func TestParseOCI(t *testing.T) {
	for _, tt := range []struct {
		name  string
		in    string
		named string
	}{
		{name: "bare", in: "nginx", named: "unikraft.io/official/nginx"},
		{name: "tagged", in: "myuser/app:v1", named: "unikraft.io/myuser/app:v1"},
		{
			name:  "oci scheme is stripped",
			in:    "oci://index.unikraft.io/me/app@" + testDigest,
			named: "index.unikraft.io/me/app@" + testDigest,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := reference.Parse(tt.in)
			require.NoError(t, err)

			require.Equal(t, schemes.OCI, ref.Scheme())
			require.False(t, ref.Scheme().IsHTTP())
			require.Equal(t, tt.named, ref.AsNamed().String())

			require.Equal(t, tt.named, ref.String())
			require.Nil(t, ref.AsURL())
		})
	}
}

func TestParseRejectsSchemes(t *testing.T) {
	for _, tt := range []struct{ in, wantErr string }{
		{"oci-layout:///tmp/layout", "addresses a local image"},
		{"oci-archive:///tmp/image.tar", "addresses a local image"},
		{"banana://x", `unsupported image URI scheme: "banana"`},
		{"https://bad", `unsupported image URI scheme: "https"`},
	} {
		_, err := reference.Parse(tt.in)
		require.ErrorContains(t, err, tt.wantErr, "input %q", tt.in)
	}

	_, err := reference.Parse("oci-archive:///tmp/image.tar")
	require.ErrorIs(t, err, reference.ErrLocalScheme)
	require.ErrorIs(t, err, schemes.ErrUnsupported)

	_, err = reference.Parse("banana://x")
	require.ErrorIs(t, err, schemes.ErrUnsupported)
	require.NotErrorIs(t, err, reference.ErrLocalScheme)
}

func TestParseErrorsAreNotDoubled(t *testing.T) {
	for _, in := range []string{"NGINX:latest", "", "nginx:BAD TAG", "http+oci://"} {
		_, err := reference.Parse(in)
		require.Error(t, err, "input %q", in)
		require.ErrorIs(t, err, reference.ErrInvalidReference, "input %q", in)
		require.NotContains(t, err.Error(), "invalid image reference: invalid image reference")
	}

	_, err := reference.Parse("oci-archive:///tmp/x.tar")
	require.NotErrorIs(t, err, reference.ErrInvalidReference)
	require.NotContains(t, err.Error(), "invalid image reference")
}

func TestFromNamed(t *testing.T) {
	named, err := distref.ParseNamed("index.unikraft.io/me/app:v1")
	require.NoError(t, err)

	ref, err := reference.FromNamed(named)
	require.NoError(t, err)
	require.Equal(t, schemes.OCI, ref.Scheme())
	require.Equal(t, "index.unikraft.io/me/app:v1", ref.String())
	require.Nil(t, ref.AsURL())

	_, err = reference.FromNamed(nil)
	require.ErrorIs(t, err, reference.ErrInvalidReference)
}

func TestSchemeErrorsReadAsSentences(t *testing.T) {
	_, err := reference.Parse("oci-archive:///tmp/x.tar")
	require.EqualError(t, err, `unsupported image URI scheme: "oci-archive" addresses a local image`)
	require.ErrorIs(t, err, reference.ErrLocalScheme)
	require.ErrorIs(t, err, schemes.ErrUnsupported)

	_, err = reference.Parse("banana://x")
	require.EqualError(t, err, `unsupported image URI scheme: "banana"`)
}

func TestFromNamedRequiresAQualifiedName(t *testing.T) {
	for _, name := range []string{"foo/bar", "official/nginx", "foo"} {
		named, err := distref.WithName(name)
		require.NoError(t, err)
		_, err = reference.FromNamed(named)
		require.ErrorIs(t, err, reference.ErrInvalidReference, "name %q", name)
	}

	named, err := distref.WithName("localhost/me/app")
	require.NoError(t, err)
	ref, err := reference.FromNamed(named)
	require.NoError(t, err, "localhost is a domain")
	require.Equal(t, "localhost/me/app", ref.String())
}
