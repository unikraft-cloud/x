// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package reference parses the identifiers that name an image: OCI registry
// references, and OCI image layouts served over HTTP.
package reference

import (
	"fmt"
	"net/url"
	"strings"

	distref "github.com/distribution/reference"
	"github.com/opencontainers/go-digest"

	"unikraft.com/x/image-spec/schemes"
)

// transport returns the URL scheme an HTTP scheme is fetched over.
func transport(s schemes.Scheme) string {
	if !s.IsHTTP() {
		return ""
	}
	transport, _, _ := strings.Cut(string(s), "+")
	return transport
}

// Reference identifies an image.
//
// It is either an OCI registry reference:
//
//	[<host>[:<port>]/]<repository>[:<tag>][@<digest>]
//
// or an OCI image layout served over HTTP:
//
//	<http|https>+oci://<host>[:<port>]/<repository>/<identifier>
//
// where <identifier> is the final path segment: a tag, or '@' and a digest.
type Reference struct {
	scheme schemes.Scheme

	domain string

	path   string
	tag    string
	digest digest.Digest
}

// Scheme returns the URI scheme the image is addressed by, never a local one.
func (r Reference) Scheme() schemes.Scheme { return r.scheme }

// Domain returns the registry host for an OCI reference, or the host serving
// the layout for an HTTP one.
func (r Reference) Domain() string { return r.domain }

// Path returns the repository, without a leading or trailing '/'.
func (r Reference) Path() string { return r.path }

// Tag returns the tag naming the image, or "" if it has none.
func (r Reference) Tag() string { return r.tag }

// Digest returns the digest naming the image, or "" if it has none.
func (r Reference) Digest() digest.Digest { return r.digest }

// IsZero reports whether r is the unset reference.
func (r Reference) IsZero() bool { return r == Reference{} }

// Equal reports whether r and other are the same reference.
func (r Reference) Equal(other Reference) bool { return r == other }

// Name returns the OCI name of the image, without any tag or digest.
func (r Reference) Name() string {
	if r.IsZero() {
		return ""
	}
	return r.domain + "/" + r.path
}

// AsNamed returns the OCI name of the image, or nil for an HTTP reference.
func (r Reference) AsNamed() distref.Named {
	if r.scheme.IsHTTP() {
		return nil
	}
	named, err := r.named()
	if err != nil {
		return nil
	}
	return named
}

func (r Reference) named() (distref.Named, error) {
	if r.IsZero() {
		return nil, fmt.Errorf("%w: no name", ErrInvalidReference)
	}
	named, err := distref.WithName(r.Name())
	if err != nil {
		return nil, err
	}
	if r.tag != "" {
		tagged, err := distref.WithTag(named, r.tag)
		if err != nil {
			return nil, err
		}
		named = tagged
	}
	if r.digest != "" {
		digested, err := distref.WithDigest(named, r.digest)
		if err != nil {
			return nil, err
		}
		named = digested
	}
	return named, nil
}

// AsURL returns the URL an HTTP reference is fetched from, or nil otherwise.
func (r Reference) AsURL() *url.URL {
	if !r.scheme.IsHTTP() || r.IsZero() {
		return nil
	}
	identifier := r.tag
	if identifier == "" {
		identifier = defaultTag
	}
	if r.digest != "" {
		identifier = "@" + r.digest.String()
	}
	return &url.URL{
		Scheme: transport(r.scheme),
		Host:   r.domain,
		Path:   "/" + r.path + "/" + identifier,
	}
}

// String returns the reference in the form Parse accepts, and the platform API
// expects. An HTTP reference is named by its digest if it has one.
func (r Reference) String() string {
	if r.IsZero() {
		return ""
	}
	if r.scheme.IsHTTP() {
		u := r.AsURL()
		u.Scheme = string(r.scheme)
		return u.String()
	}
	name := r.Name()
	if r.tag != "" {
		name += ":" + r.tag
	}
	if r.digest != "" {
		name += "@" + r.digest.String()
	}
	return name
}

// FormatOpts is how a reference is rendered for display.
type FormatOpts struct {
	// OmitDigest removes the digest, for concise output such as a table cell.
	OmitDigest bool

	// DefaultDomain and DefaultPrefix are removed when present.
	DefaultDomain string
	DefaultPrefix string
}

// Format renders r in the short form a human reads. An HTTP reference is
// rendered as its URI.
func (r Reference) Format(o FormatOpts) string {
	if r.IsZero() {
		return ""
	}
	if r.scheme.IsHTTP() {
		if o.OmitDigest && r.tag != "" {
			r.digest = ""
		}
		return r.String()
	}

	domain, repository := r.domain, r.path
	if domain == o.DefaultDomain {
		candidate, trimmed := repository, false

		if o.DefaultPrefix != "" && strings.HasPrefix(candidate, o.DefaultPrefix) {
			if rest := strings.TrimPrefix(candidate, o.DefaultPrefix); !strings.ContainsRune(rest, '/') {
				candidate, trimmed = rest, true
			}
		}

		if trimmed || strings.ContainsRune(candidate, '/') {
			domain, repository = "", candidate
		}
	}

	out := repository
	if domain != "" {
		out = domain + "/" + repository
	}
	if r.tag != "" {
		out += ":" + r.tag
	}

	if r.digest != "" && !o.OmitDigest {
		out += "@" + r.digest.String()
	}
	return out
}

// WithTag returns the reference with its tag set to tag. Like
// [distref.WithTag], the reference keeps its digest.
func (r Reference) WithTag(tag string) (Reference, error) {
	if r.IsZero() {
		return Reference{}, fmt.Errorf("%w: no name", ErrInvalidReference)
	}
	named, err := distref.WithName(r.Name())
	if err != nil {
		return Reference{}, fmt.Errorf("%w: %w", ErrInvalidReference, err)
	}
	if _, err := distref.WithTag(named, tag); err != nil {
		return Reference{}, fmt.Errorf("%w: %w", ErrInvalidReference, err)
	}
	r.tag = tag
	return r, nil
}

// WithDigest returns the reference with its digest set to dgst. Like
// [distref.WithDigest], the reference keeps its tag.
func (r Reference) WithDigest(dgst digest.Digest) (Reference, error) {
	if r.IsZero() {
		return Reference{}, fmt.Errorf("%w: no name", ErrInvalidReference)
	}
	if err := dgst.Validate(); err != nil {
		return Reference{}, fmt.Errorf("%w: %w", ErrInvalidReference, err)
	}
	r.digest = dgst
	return r, nil
}

// WithoutTag returns the reference with its tag removed. Together with
// WithoutDigest it is [distref.TrimNamed].
func (r Reference) WithoutTag() Reference {
	r.tag = ""
	return r
}

// WithoutDigest returns the reference with its digest removed.
func (r Reference) WithoutDigest() Reference {
	r.digest = ""
	return r
}

// WithDomain returns the reference in domain instead. Only an OCI reference
// canonicalizes index.docker.io to docker.io.
func (r Reference) WithDomain(domain string) (Reference, error) {
	if r.IsZero() {
		return Reference{}, fmt.Errorf("%w: no name", ErrInvalidReference)
	}
	if r.scheme.IsHTTP() {
		host, err := normalizeHost(domain, r.scheme)
		if err != nil {
			return Reference{}, fmt.Errorf("%w: %w", ErrInvalidReference, err)
		}
		domain = host
	} else if domain == legacyDockerDomain {
		domain = dockerDomain
	}
	if !isDomain(domain) {
		return Reference{}, fmt.Errorf("%w: %q is not a registry host", ErrInvalidReference, domain)
	}
	named, err := distref.WithName(domain + "/" + r.path)
	if err != nil {
		return Reference{}, fmt.Errorf("%w: %w", ErrInvalidReference, err)
	}
	if distref.Domain(named) != domain {
		return Reference{}, fmt.Errorf("%w: %q is not a registry host", ErrInvalidReference, domain)
	}
	r.domain = domain
	return r, nil
}

// WithDefaultTag returns the reference with an implicit "latest" tag applied
// when it names neither a tag nor a digest, like [distref.TagNameOnly].
func (r Reference) WithDefaultTag() Reference {
	if r.IsZero() || r.scheme.IsHTTP() || r.tag != "" || r.digest != "" {
		return r
	}
	r.tag = defaultTag
	return r
}

// WithoutDefaultTag returns the reference with an explicit "latest" tag
// removed, the inverse of WithDefaultTag.
func (r Reference) WithoutDefaultTag() Reference {
	if r.IsZero() || r.scheme.IsHTTP() || r.tag != defaultTag {
		return r
	}
	r.tag = ""
	return r
}

// Matches reports whether r is the image addressed by pattern.
func (r Reference) Matches(pattern Reference) bool {
	if r.IsZero() || pattern.IsZero() {
		return false
	}
	if pattern.scheme != r.scheme || pattern.Name() != r.Name() {
		return false
	}
	if pattern.digest != "" {
		return pattern.digest == r.digest
	}
	if pattern.tag != "" {
		return pattern.tag == r.tag
	}
	return true
}
