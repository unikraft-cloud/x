// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package imageref parses the identifiers that name an image: OCI registry
// references, and OCI image layouts served over HTTP.
package imageref

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"strings"

	ociref "github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
)

// Scheme is the URI scheme an image is addressed by.
type Scheme string

const (
	// SchemeOCI addresses an image in an OCI registry.
	SchemeOCI Scheme = "oci"

	// SchemeOCILayout and SchemeOCIArchive address an image on the local
	// filesystem, as a directory and as a tarball respectively.
	SchemeOCILayout  Scheme = "oci-layout"
	SchemeOCIArchive Scheme = "oci-archive"

	// SchemeHTTPOCI and SchemeHTTPSOCI address an OCI image layout tarball
	// served over HTTP and HTTPS respectively.
	SchemeHTTPOCI  Scheme = "http+oci"
	SchemeHTTPSOCI Scheme = "https+oci"
)

// IsHTTP reports whether s addresses a layout served over HTTP.
func (s Scheme) IsHTTP() bool {
	return s == SchemeHTTPOCI || s == SchemeHTTPSOCI
}

// transport returns the URL scheme a layout in s is fetched over, or "" for a
// scheme that is not fetched over HTTP.
func (s Scheme) transport() string {
	if !s.IsHTTP() {
		return ""
	}
	transport, _, _ := strings.Cut(string(s), "+")
	return transport
}

var (
	// ErrUnsupportedScheme is returned for a URI scheme that does not name an
	// image this package can address.
	ErrUnsupportedScheme = errors.New("unsupported image URI scheme")

	// ErrLocalScheme is returned for a scheme that addresses the local
	// filesystem.
	ErrLocalScheme = errors.New("addresses a local image")

	// ErrInvalidReference wraps every rejection by the reference.
	ErrInvalidReference = errors.New("invalid image reference")
)

// ParseScheme returns the Scheme named by s.
func ParseScheme(s string) (Scheme, error) {
	switch scheme := Scheme(s); scheme {
	case SchemeOCI, SchemeOCILayout, SchemeOCIArchive, SchemeHTTPOCI, SchemeHTTPSOCI:
		return scheme, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedScheme, s)
	}
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
// where <identifier> is the final path segment, and names a tag unless it is
// prefixed with '@', in which case it names a digest. Keeping the identifier in
// the path is what lets such a URI double as the URL the layout is fetched
// from.
type Reference struct {
	scheme Scheme

	// domain is the registry host for an OCI reference, and the host[:port]
	// serving the layout for an HTTP one.
	domain string

	// path is the repository, without a leading or trailing '/'.
	path string

	// tag and digest name the image within the repository.
	tag    string
	digest digest.Digest
}

type parseOptions struct {
	defaultDomain string
	defaultPrefix string
}

// ParseOpt sets the registry domain and repository prefix that an identifier
// leaves implicit. Parse adds them and Format takes them away again.
type ParseOpt func(*parseOptions)

// WithDefaultDomain sets the registry domain to assume for an identifier that
// does not name one. An empty domain is ignored.
func WithDefaultDomain(domain string) ParseOpt {
	return func(o *parseOptions) {
		if domain != "" {
			o.defaultDomain = domain
		}
	}
}

// WithDefaultPrefix sets the repository prefix to assume for a single-segment
// repository on the default domain, for example "official/". A trailing '/' is
// added if absent. An empty prefix is ignored.
func WithDefaultPrefix(prefix string) ParseOpt {
	return func(o *parseOptions) {
		if prefix == "" {
			return
		}
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		o.defaultPrefix = prefix
	}
}

func newParseOptions(opts []ParseOpt) parseOptions {
	o := parseOptions{defaultDomain: dockerDomain, defaultPrefix: officialRepoPrefix}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// Parse parses an image identifier: a bare OCI reference, or a URI in one of
// the schemes above. The options apply to the OCI half only.
func Parse(s string, opts ...ParseOpt) (Reference, error) {
	scheme, rest, hasScheme := strings.Cut(s, "://")
	if !hasScheme {
		return fromName(newParseOptions(opts), s)
	}

	parsed, err := ParseScheme(scheme)
	if err != nil {
		return Reference{}, err
	}

	switch {
	case parsed == SchemeOCI:
		return fromName(newParseOptions(opts), rest)
	case parsed.IsHTTP():
		ref, err := fromHTTP(parsed, rest)
		if err != nil {
			return Reference{}, fmt.Errorf("%w %q: %w", ErrInvalidReference, s, err)
		}
		return ref, nil
	default:
		return Reference{}, fmt.Errorf("%w: %q %w", ErrUnsupportedScheme, scheme, ErrLocalScheme)
	}
}

// FromNamed returns a reference for an image that is already named, which is
// always an OCI registry reference.
func FromNamed(named ociref.Named) (Reference, error) {
	if named == nil {
		return Reference{}, fmt.Errorf("%w: no name", ErrInvalidReference)
	}
	domain := ociref.Domain(named)
	if domain != localhost &&
		!strings.ContainsAny(domain, ".:") &&
		strings.ToLower(domain) == domain {
		// Domain splits on the first '/' without deciding whether what precedes
		// it is domain-shaped, so an un-normalized name yields a reference whose
		// String names a different image.
		return Reference{}, fmt.Errorf("%w: %q is not fully qualified", ErrInvalidReference, named.Name())
	}
	ref := Reference{
		scheme: SchemeOCI,
		domain: domain,
		path:   ociref.Path(named),
	}
	if tagged, ok := named.(ociref.Tagged); ok {
		ref.tag = tagged.Tag()
	}
	if digested, ok := named.(ociref.Digested); ok {
		ref.digest = digested.Digest()
	}
	return ref, nil
}

func fromName(o parseOptions, s string) (Reference, error) {
	named, err := parseNormalizedNamed(s, o)
	if err != nil {
		return Reference{}, fmt.Errorf("%w: %w", ErrInvalidReference, err)
	}
	return FromNamed(named)
}

func fromHTTP(scheme Scheme, rest string) (Reference, error) {
	if strings.ContainsRune(rest, '%') {
		return Reference{}, errors.New("must not percent-encode characters")
	}

	if strings.ContainsRune(rest, '#') {
		return Reference{}, errors.New("must not have a fragment")
	}

	parsed, err := url.Parse(scheme.transport() + "://" + rest)
	if err != nil {
		return Reference{}, err
	}

	// Reject the parts of a URL that we would otherwise silently drop.
	switch {
	case parsed.User != nil:
		return Reference{}, errors.New("must not embed credentials")
	case parsed.RawQuery != "" || parsed.ForceQuery:
		return Reference{}, errors.New("must not have a query")
	}

	host, err := normalizeHost(parsed.Host, scheme)
	if err != nil {
		return Reference{}, err
	}

	dir, identifier := path.Split(parsed.Path)
	if identifier == "" {
		if strings.HasSuffix(parsed.Path, "/") {
			return Reference{}, errors.New("unexpected terminating '/'")
		}
		return Reference{}, errors.New("missing a repository and identifier")
	}

	repository := strings.Trim(dir, "/")
	if repository == "" {
		return Reference{}, fmt.Errorf("missing a repository name before %q", identifier)
	}

	if cleaned := path.Clean(dir); cleaned != strings.TrimSuffix(dir, "/") {
		return Reference{}, errors.New("repository must not contain empty or relative path segments")
	}

	if !strings.ContainsRune(repository, '/') {
		return Reference{}, fmt.Errorf("repository %q must be <namespace>/<name>", repository)
	}

	ref := Reference{scheme: scheme, domain: host, path: repository}

	named, err := ociref.WithName(host + "/" + repository)
	if err != nil {
		return Reference{}, err
	}

	if identifier[0] == '@' {
		dgst, err := digest.Parse(identifier[1:])
		if err != nil {
			return Reference{}, err
		}
		if _, err := ociref.WithDigest(named, dgst); err != nil {
			return Reference{}, err
		}
		ref.digest = dgst
		return ref, nil
	}
	if _, err := ociref.WithTag(named, identifier); err != nil {
		return Reference{}, err
	}
	ref.tag = identifier
	return ref, nil
}

// normalizeHost lower-cases the authority and drops the transport's default
// port, so that two URIs naming the same host compare equal.
func normalizeHost(host string, scheme Scheme) (string, error) {
	if host == "" {
		return "", errors.New("missing host")
	}
	host = strings.ToLower(host)

	// A ':' after the last ']' separates a port; one before it is IPv6.
	if strings.LastIndexByte(host, ':') <= strings.LastIndexByte(host, ']') {
		return host, nil
	}

	name, port, err := net.SplitHostPort(host)
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", errors.New("missing host")
	}
	if port == "" {
		return "", errors.New("missing port after ':'")
	}

	defaultPort := "80"
	if scheme == SchemeHTTPSOCI {
		defaultPort = "443"
	}
	if port != defaultPort {
		return host, nil
	}
	if strings.ContainsRune(name, ':') {
		// An IPv6 literal keeps its brackets.
		return "[" + name + "]", nil
	}
	return name, nil
}

// Scheme returns the URI scheme the image is addressed by. It is never a local
// scheme, which Parse rejects.
func (r Reference) Scheme() Scheme { return r.scheme }

// Domain returns the registry host for an OCI reference, or the host serving
// the layout for an HTTP one.
func (r Reference) Domain() string { return r.domain }

// Path returns the repository, without a leading or trailing '/'.
func (r Reference) Path() string { return r.path }

// Tag returns the tag naming the image, or "" if it is named by digest alone.
func (r Reference) Tag() string { return r.tag }

// Digest returns the digest naming the image, or "" if it is named by tag
// alone.
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
	if r.domain == "" {
		return r.path
	}
	return r.domain + "/" + r.path
}

// Identifier returns the tag or "@"-prefixed digest naming the image within its
// repository, which for an HTTP reference is the final segment of its URL.
func (r Reference) Identifier() string {
	if r.digest != "" && r.tag == "" {
		return "@" + r.digest.String()
	}
	return r.tag
}

// Named returns the OCI name of the image, or nil if it has none.
// An HTTP reference has none.
func (r Reference) Named() ociref.Named {
	if r.scheme.IsHTTP() {
		return nil
	}
	named, err := r.named()
	if err != nil {
		return nil
	}
	return named
}

func (r Reference) named() (ociref.Named, error) {
	if r.IsZero() {
		return nil, fmt.Errorf("%w: no name", ErrInvalidReference)
	}
	named, err := ociref.WithName(r.Name())
	if err != nil {
		return nil, err
	}
	if r.tag != "" {
		tagged, err := ociref.WithTag(named, r.tag)
		if err != nil {
			return nil, err
		}
		named = tagged
	}
	if r.digest != "" {
		digested, err := ociref.WithDigest(named, r.digest)
		if err != nil {
			return nil, err
		}
		named = digested
	}
	return named, nil
}

// URL returns where the image layout is fetched from, or nil for an image in a
// registry, which is resolved against the registry instead.
func (r Reference) URL() *url.URL {
	if !r.scheme.IsHTTP() || r.IsZero() {
		return nil
	}
	return &url.URL{
		Scheme: r.scheme.transport(),
		Host:   r.domain,
		Path:   "/" + r.path + "/" + r.Identifier(),
	}
}

// String returns the identifier the image is addressed by, which round-trips
// through Parse. An OCI reference is returned without its "oci://" prefix,
// since that is the form registries and the platform API expect.
func (r Reference) String() string {
	if r.IsZero() {
		return ""
	}
	if r.scheme.IsHTTP() {
		return string(r.scheme) + "://" + r.domain + "/" + r.path + "/" + r.Identifier()
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

// Format renders r in the short form a human reads.
// An HTTP reference is returned as the URI it is fetched from: its host and
// transport are part of what names it.
func (r Reference) Format(o FormatOpts) string {
	if r.IsZero() {
		return ""
	}
	if r.scheme.IsHTTP() {
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

// WithTag returns the reference named by tag instead, dropping any digest.
func (r Reference) WithTag(tag string) (Reference, error) {
	if r.IsZero() {
		return Reference{}, fmt.Errorf("%w: no name", ErrInvalidReference)
	}
	named, err := ociref.WithName(r.Name())
	if err != nil {
		return Reference{}, fmt.Errorf("%w: %w", ErrInvalidReference, err)
	}
	if _, err := ociref.WithTag(named, tag); err != nil {
		return Reference{}, fmt.Errorf("%w: %w", ErrInvalidReference, err)
	}
	r.tag, r.digest = tag, ""
	return r, nil
}

// WithDigest returns the reference named by dgst instead, dropping any tag.
func (r Reference) WithDigest(dgst digest.Digest) (Reference, error) {
	if r.IsZero() {
		return Reference{}, fmt.Errorf("%w: no name", ErrInvalidReference)
	}
	if err := dgst.Validate(); err != nil {
		return Reference{}, fmt.Errorf("%w: %w", ErrInvalidReference, err)
	}
	r.tag, r.digest = "", dgst
	return r, nil
}

// WithDomain returns the reference in domain instead, which for an HTTP
// reference also changes the host its layout is fetched from.
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
	}
	if domain == legacyDockerDomain {
		domain = dockerDomain
	}
	if domain != localhost &&
		!strings.ContainsAny(domain, ".:") &&
		strings.ToLower(domain) == domain {
		return Reference{}, fmt.Errorf("%w: %q is not a registry host", ErrInvalidReference, domain)
	}
	if _, err := ociref.WithName(domain + "/" + r.path); err != nil {
		return Reference{}, fmt.Errorf("%w: %w", ErrInvalidReference, err)
	}
	r.domain = domain
	return r, nil
}

// WithDefaultTag returns the reference with an implicit "latest" tag applied
// when it names neither a tag nor a digest.
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
	if r.scheme.IsHTTP() || pattern.scheme.IsHTTP() {
		return r == pattern
	}

	if name := pattern.Name(); name != r.Name() && name != r.path {
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
