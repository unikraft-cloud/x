// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imageref_test

import (
	"strings"
	"testing"

	ociref "github.com/distribution/reference"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/image-spec/imageref"
)

// testDigest is an arbitrary but well-formed digest.
const testDigest = "sha256:43d3d758e6fba7d4734ac142cfdbf8aa786fcbbfd828017eecaadc5140a4b190"

func TestParseHTTP(t *testing.T) {
	tests := []struct {
		name    string
		uri     string
		scheme  imageref.Scheme
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
			scheme: imageref.SchemeHTTPSOCI,
			domain: "username.unikraftcdn.com",
			path:   "some/path/to/layout",
			url:    "https://username.unikraftcdn.com/some/path/to/layout/latest",
			tag:    "latest",
		},
		{
			name:   "digest",
			uri:    "https+oci://username.unikraftcdn.com/some/path/to/layout/@" + testDigest,
			scheme: imageref.SchemeHTTPSOCI,
			domain: "username.unikraftcdn.com",
			path:   "some/path/to/layout",
			url:    "https://username.unikraftcdn.com/some/path/to/layout/@" + testDigest,
			digest: testDigest,
		},
		{
			name:   "host with port",
			uri:    "http+oci://localhost:8001/root/helloworld/latest",
			scheme: imageref.SchemeHTTPOCI,
			domain: "localhost:8001",
			path:   "root/helloworld",
			url:    "http://localhost:8001/root/helloworld/latest",
			tag:    "latest",
		},
		{
			name:   "multi-label host",
			uri:    "http+oci://layouts.internal.example.com/test/registry/latest",
			scheme: imageref.SchemeHTTPOCI,
			domain: "layouts.internal.example.com",
			path:   "test/registry",
			url:    "http://layouts.internal.example.com/test/registry/latest",
			tag:    "latest",
		},
		{
			name:   "host without a dot",
			uri:    "http+oci://myhost/repo/name/latest",
			scheme: imageref.SchemeHTTPOCI,
			domain: "myhost",
			path:   "repo/name",
			url:    "http://myhost/repo/name/latest",
			tag:    "latest",
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
			// The platform requires a namespace, so this is rejected here
			// rather than by the API after a round trip.
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
			// An escaped separator would otherwise be decoded before the
			// identifier is split off, so the name and the URL it is fetched
			// from would disagree about where the repository ends.
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
			// The repository is cleaned on its way into the name but the URL is
			// fetched verbatim, so these two would name different repositories.
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
			// A bare '?' lands in ForceQuery, not RawQuery, so inspecting the
			// parsed parts alone would let it through - and then URL.String()
			// re-emits it while String() drops it.
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
			// A bare '#' is discarded by url.Parse entirely, so it has to be
			// caught before parsing.
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
			ref, err := imageref.Parse(tt.uri)
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

			// An HTTP reference has no OCI name.
			require.Nil(t, ref.Named())
			require.Equal(t, tt.url, ref.URL().String())
			require.Equal(t, tt.tag, ref.Tag())
			require.Equal(t, tt.digest, ref.Digest().String())

			// String is the identifier the image is addressed by, so it has to
			// round-trip: the platform is handed back what the user typed.
			require.Equal(t, tt.uri, ref.String())
			again, err := imageref.Parse(ref.String())
			require.NoError(t, err)
			require.Equal(t, ref, again, "parsing String() must yield an equal reference")

			// The identifier is mandatory in this grammar, so there is never an
			// implicit tag to apply and never a default tag to elide.
			require.Equal(t, ref, ref.WithDefaultTag())
			require.Equal(t, ref, ref.WithoutDefaultTag())

			// The digest is the identifier, so concise display keeps it.
			require.Equal(t, tt.uri, ref.Format(imageref.FormatOpts{OmitDigest: true}))
		})
	}
}

// A host is matched case-insensitively and the transport's default port is
// implied, so a caller that types either still addresses the same image.
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
			ref, err := imageref.Parse(tt.in)
			require.NoError(t, err)
			require.Equal(t, tt.want, ref.String())

			// Normalization has to be idempotent, or String() would not be a
			// fixed point and lookups would still miss.
			again, err := imageref.Parse(ref.String())
			require.NoError(t, err)
			require.Equal(t, ref, again)
		})
	}

	// The point of normalizing: two spellings of one image are one reference.
	a, err := imageref.Parse("http+oci://CDN.example.com:80/me/app/latest")
	require.NoError(t, err)
	b, err := imageref.Parse("http+oci://cdn.example.com/me/app/latest")
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
		{name: "bare", in: "nginx", named: "docker.io/library/nginx"},
		{name: "tagged", in: "myuser/app:v1", named: "docker.io/myuser/app:v1"},
		{
			name:  "oci scheme is stripped",
			in:    "oci://index.unikraft.io/me/app@" + testDigest,
			named: "index.unikraft.io/me/app@" + testDigest,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := imageref.Parse(tt.in)
			require.NoError(t, err)

			require.Equal(t, imageref.SchemeOCI, ref.Scheme())
			require.False(t, ref.Scheme().IsHTTP())
			require.Equal(t, tt.named, ref.Named().String())

			// An OCI reference goes on the wire without its scheme, which is
			// the form registries and the platform API expect.
			require.Equal(t, tt.named, ref.String())
			require.Nil(t, ref.URL())
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
		_, err := imageref.Parse(tt.in)
		require.ErrorContains(t, err, tt.wantErr, "input %q", tt.in)
	}

	_, err := imageref.Parse("oci-archive:///tmp/image.tar")
	require.ErrorIs(t, err, imageref.ErrLocalScheme)
	// A local scheme is a scheme this package cannot address, so one check
	// covers both.
	require.ErrorIs(t, err, imageref.ErrUnsupportedScheme)

	_, err = imageref.Parse("banana://x")
	require.ErrorIs(t, err, imageref.ErrUnsupportedScheme)
	require.NotErrorIs(t, err, imageref.ErrLocalScheme)
}

// A rejected identifier reports one reason, not the same reason twice: the CLI
// used to prefix these with a label the error already carried.
func TestParseErrorsAreNotDoubled(t *testing.T) {
	for _, in := range []string{"NGINX:latest", "", "nginx:BAD TAG", "http+oci://"} {
		_, err := imageref.Parse(in)
		require.Error(t, err, "input %q", in)
		require.ErrorIs(t, err, imageref.ErrInvalidReference, "input %q", in)
		require.NotContains(t, err.Error(), "invalid image reference: invalid image reference")
	}

	// A scheme this package cannot address is not a malformed reference, and
	// must not be reported as one.
	_, err := imageref.Parse("oci-archive:///tmp/x.tar")
	require.NotErrorIs(t, err, imageref.ErrInvalidReference)
	require.NotContains(t, err.Error(), "invalid image reference")
}

func TestWithDefaultTag(t *testing.T) {
	ref, err := imageref.Parse("nginx")
	require.NoError(t, err)
	require.Empty(t, ref.Tag())

	tagged := ref.WithDefaultTag()
	require.Equal(t, "latest", tagged.Tag())
	require.Equal(t, "docker.io/library/nginx:latest", tagged.String())

	// Applying it twice changes nothing, and the inverse undoes it exactly.
	require.Equal(t, tagged, tagged.WithDefaultTag())
	require.Equal(t, ref, tagged.WithoutDefaultTag())
}

// WithoutDefaultTag is where a caller's "every image reads :latest is noise"
// display policy attaches, without making the shared renderer lossy.
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
			ref, err := imageref.Parse(tt.in)
			require.NoError(t, err)

			trimmed := ref.WithoutDefaultTag()
			require.Equal(t, tt.want, trimmed.String())

			// Values, so the receiver cannot have been mutated.
			require.Equal(t, tt.in, ref.String())
		})
	}
}

func TestFormat(t *testing.T) {
	const (
		domain = "unikraft.io"
		prefix = "official/"
	)
	opts := imageref.FormatOpts{DefaultDomain: domain, DefaultPrefix: prefix}

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
			// An HTTP reference is named by where it is served from, so there
			// is nothing implicit to remove.
			name:      "http renders as its URI",
			in:        "https+oci://example.org/me/app/@" + testDigest,
			want:      "https+oci://example.org/me/app/@" + testDigest,
			wantShort: "https+oci://example.org/me/app/@" + testDigest,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := imageref.Parse(tt.in, imageref.WithDefaultDomain(domain), imageref.WithDefaultPrefix(prefix))
			require.NoError(t, err)

			require.Equal(t, tt.want, ref.Format(opts))

			short := opts
			short.OmitDigest = true
			require.Equal(t, tt.wantShort, ref.Format(short))

			// The long form has to re-parse to the same image, or a caller
			// copying it out of our output would address something else.
			back, err := imageref.Parse(tt.want, imageref.WithDefaultDomain(domain), imageref.WithDefaultPrefix(prefix))
			require.NoError(t, err)
			require.Equal(t, ref, back, "Format is not round-tripping")
		})
	}

	require.Empty(t, imageref.Reference{}.Format(opts))
}

func TestMatches(t *testing.T) {
	parse := func(t *testing.T, s string) imageref.Reference {
		t.Helper()
		ref, err := imageref.Parse(s, imageref.WithDefaultDomain("unikraft.io"), imageref.WithDefaultPrefix("official/"))
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
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, parse(t, tt.subject).Matches(parse(t, tt.pattern)))
		})
	}

	// Digests are compared when the pattern carries one.
	subject := parse(t, "unikraft.io/official/nginx@"+testDigest)
	require.True(t, subject.Matches(parse(t, "unikraft.io/official/nginx@"+testDigest)))
	require.False(t, subject.Matches(parse(t, "unikraft.io/official/nginx@sha256:"+
		"0000000000000000000000000000000000000000000000000000000000000000")))

	// The unset reference addresses nothing and is addressed by nothing.
	require.False(t, imageref.Reference{}.Matches(subject))
	require.False(t, subject.Matches(imageref.Reference{}))
}

func TestWithTagAndWithDigest(t *testing.T) {
	for _, in := range []string{
		"unikraft.io/official/nginx:v1",
		"https+oci://example.org/me/app/latest",
	} {
		t.Run(in, func(t *testing.T) {
			ref, err := imageref.Parse(in)
			require.NoError(t, err)

			tagged, err := ref.WithTag("v2")
			require.NoError(t, err)
			require.Equal(t, "v2", tagged.Tag())
			require.Empty(t, tagged.Digest())

			digested, err := tagged.WithDigest(testDigest)
			require.NoError(t, err)
			require.Equal(t, testDigest, digested.Digest().String())
			require.Empty(t, digested.Tag(), "a digest replaces the tag it is named by")

			// Both round-trip, which is what makes them usable on an HTTP
			// reference whose identifier lives in its path.
			back, err := imageref.Parse(digested.String())
			require.NoError(t, err)
			require.Equal(t, digested, back)

			// The receiver is untouched.
			require.Equal(t, in, ref.String())
		})
	}

	ref, err := imageref.Parse("nginx")
	require.NoError(t, err)
	_, err = ref.WithTag("not a tag")
	require.ErrorIs(t, err, imageref.ErrInvalidReference)
	_, err = ref.WithDigest("sha256:nothex")
	require.ErrorIs(t, err, imageref.ErrInvalidReference)
}

func TestFromNamed(t *testing.T) {
	named, err := ociref.ParseNamed("index.unikraft.io/me/app:v1")
	require.NoError(t, err)

	ref, err := imageref.FromNamed(named)
	require.NoError(t, err)
	require.Equal(t, imageref.SchemeOCI, ref.Scheme())
	require.Equal(t, "index.unikraft.io/me/app:v1", ref.String())
	require.Nil(t, ref.URL())

	// A missing name is an error rather than a silently unusable reference.
	_, err = imageref.FromNamed(nil)
	require.ErrorIs(t, err, imageref.ErrInvalidReference)
}

func TestReferenceIsComparable(t *testing.T) {
	a, err := imageref.Parse("nginx:latest")
	require.NoError(t, err)
	b, err := imageref.Parse("nginx:latest")
	require.NoError(t, err)

	// Compared with == deliberately, and via a variable so that testifylint does
	// not rewrite it.
	sameImage := a == b
	require.True(t, sameImage, "two references to the same image must be equal")
	require.True(t, a.Equal(b))

	seen := map[imageref.Reference]struct{}{a: {}}
	_, ok := seen[b]
	require.True(t, ok, "an equal reference must find its own map entry")

	c, err := imageref.Parse("nginx:v1")
	require.NoError(t, err)
	differentImage := a == c
	require.False(t, differentImage, "references to different images must not be equal")
}

// Every accessor is safe on the unset reference, so a caller holding an image
// that was never set does not have to guard each one.
func TestZeroReference(t *testing.T) {
	var ref imageref.Reference

	require.True(t, ref.IsZero())
	require.Empty(t, ref.Scheme())
	require.Empty(t, ref.Domain())
	require.Empty(t, ref.Path())
	require.Empty(t, ref.Tag())
	require.Empty(t, ref.Digest())
	require.Empty(t, ref.Name())
	require.Empty(t, ref.Identifier())
	require.Nil(t, ref.Named())
	require.Nil(t, ref.URL())
	require.Empty(t, ref.String())
	require.Empty(t, ref.Format(imageref.FormatOpts{}))
	require.Equal(t, ref, ref.WithDefaultTag())
	require.Equal(t, ref, ref.WithoutDefaultTag())

	_, err := ref.WithTag("v1")
	require.ErrorIs(t, err, imageref.ErrInvalidReference)
	_, err = ref.WithDigest(testDigest)
	require.ErrorIs(t, err, imageref.ErrInvalidReference)
}

// A registry reachable under two names yields two distinct references, which
// then neither compare equal nor deduplicate. WithDomain is how a caller moves
// one onto the canonical spelling.
func TestWithDomain(t *testing.T) {
	ref, err := imageref.Parse("index.unikraft.io/official/nginx:v1")
	require.NoError(t, err)

	moved, err := ref.WithDomain("unikraft.io")
	require.NoError(t, err)
	require.Equal(t, "unikraft.io/official/nginx:v1", moved.String())
	require.Equal(t, "index.unikraft.io/official/nginx:v1", ref.String(), "the receiver is a value")

	canonical, err := imageref.Parse("unikraft.io/official/nginx:v1")
	require.NoError(t, err)
	require.Equal(t, canonical, moved)

	// An HTTP reference moves host too, and the new host is normalized like any
	// other.
	http, err := imageref.Parse("https+oci://cdn.example.com/me/app/latest")
	require.NoError(t, err)
	moved, err = http.WithDomain("OTHER.example.com:443")
	require.NoError(t, err)
	require.Equal(t, "https+oci://other.example.com/me/app/latest", moved.String())

	_, err = ref.WithDomain("NOT A HOST")
	require.ErrorIs(t, err, imageref.ErrInvalidReference)
	_, err = imageref.Reference{}.WithDomain("unikraft.io")
	require.ErrorIs(t, err, imageref.ErrInvalidReference)
}

// Storing a reference as components rather than a parsed name is only sound if
// the components reassemble to what was parsed.
func TestStringIsAFixedPoint(t *testing.T) {
	for _, in := range []string{
		// registry references
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

		// HTTP layouts
		"http+oci://cdn.example.com/me/app/latest",
		"https+oci://cdn.example.com/me/app/@" + testDigest,
		"http+oci://cdn.example.com:8080/me/app/latest",
		"http+oci://[::1]:8080/me/app/latest",
		"http+oci://127.0.0.1:8080/me/app/latest",
		"https+oci://a.b.c.example.com/one/two/three/four/latest",
		"http+oci://cdn.example.com/me/app/v1.2.3-alpha_4",
	} {
		t.Run(in, func(t *testing.T) {
			ref, err := imageref.Parse(in)
			require.NoError(t, err)

			// One pass has to reach the fixed point, and a second must not move.
			once := ref.String()
			again, err := imageref.Parse(once)
			require.NoError(t, err)
			require.Equal(t, ref, again, "%q -> %q did not re-parse to an equal reference", in, once)
			require.Equal(t, once, again.String(), "String() is not a fixed point")

			// Named and URL are rebuilt from the same components, so they have to
			// agree with String rather than drift from it.
			if ref.Scheme().IsHTTP() {
				require.Nil(t, ref.Named())
				transport := strings.TrimSuffix(string(ref.Scheme()), "+oci")
				require.Equal(t, transport+"://"+ref.Domain()+"/"+ref.Path()+"/"+ref.Identifier(),
					ref.URL().String())
			} else {
				require.NotNil(t, ref.Named())
				require.Nil(t, ref.URL())
			}
		})
	}
}

// An unusable scheme has to read as one sentence.
func TestSchemeErrorsReadAsSentences(t *testing.T) {
	_, err := imageref.Parse("oci-archive:///tmp/x.tar")
	require.EqualError(t, err, `unsupported image URI scheme: "oci-archive" addresses a local image`)
	require.ErrorIs(t, err, imageref.ErrLocalScheme)
	require.ErrorIs(t, err, imageref.ErrUnsupportedScheme)

	_, err = imageref.Parse("banana://x")
	require.EqualError(t, err, `unsupported image URI scheme: "banana"`)
}

// Format must never render a name that addresses a different image.
func TestFormatDoesNotRenderADifferentImage(t *testing.T) {
	const (
		domain = "unikraft.io"
		prefix = "official/"
	)
	opts := imageref.FormatOpts{DefaultDomain: domain, DefaultPrefix: prefix}

	for _, tt := range []struct{ name, want string }{
		{"unikraft.io/official/nginx", "nginx"},
		{"unikraft.io/me/app", "me/app"},
		{"unikraft.io/official/utils/volimport", "official/utils/volimport"},
		// The interesting one: bare "nginx" would read back as
		// "unikraft.io/official/nginx".
		{"unikraft.io/nginx", "unikraft.io/nginx"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			named, err := ociref.WithName(tt.name)
			require.NoError(t, err)
			ref, err := imageref.FromNamed(named)
			require.NoError(t, err)

			require.Equal(t, tt.want, ref.Format(opts))
		})
	}
}

// The components have to reassemble to the name they were taken from, so a
// domain has to be domain-shaped by the same test the parser applies.
func TestFromNamedRequiresAQualifiedName(t *testing.T) {
	for _, name := range []string{"foo/bar", "official/nginx", "foo"} {
		named, err := ociref.WithName(name)
		require.NoError(t, err)
		_, err = imageref.FromNamed(named)
		require.ErrorIs(t, err, imageref.ErrInvalidReference, "name %q", name)
	}

	named, err := ociref.WithName("localhost/me/app")
	require.NoError(t, err)
	ref, err := imageref.FromNamed(named)
	require.NoError(t, err, "localhost is a domain")
	require.Equal(t, "localhost/me/app", ref.String())
}

func TestWithDomainRejectsANonHost(t *testing.T) {
	ref, err := imageref.Parse("nginx")
	require.NoError(t, err)

	// "myhost" would be read back as the first segment of the repository.
	_, err = ref.WithDomain("myhost")
	require.ErrorIs(t, err, imageref.ErrInvalidReference)

	// The legacy Docker Index domain is aliased, so the result does not read
	// back as a different domain than it was given.
	moved, err := ref.WithDomain("index.docker.io")
	require.NoError(t, err)
	require.Equal(t, "docker.io/library/nginx", moved.String())

	back, err := imageref.Parse(moved.String())
	require.NoError(t, err)
	require.Equal(t, moved, back)
}
