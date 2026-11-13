// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package reference

import (
	// go-digest only accepts sha256 digests if something links the hash in.
	_ "crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"strings"

	distref "github.com/distribution/reference"
	"github.com/opencontainers/go-digest"

	"unikraft.com/x/image-spec/schemes"
)

var (
	// ErrLocalScheme is returned for a scheme that addresses the local
	// filesystem.
	ErrLocalScheme = errors.New("addresses a local image")

	// ErrInvalidReference wraps every rejection of a malformed reference.
	ErrInvalidReference = errors.New("invalid image reference")
)

type parseOptions struct {
	defaultDomain string
}

// ParseOpt changes what an identifier leaves implicit.
type ParseOpt func(*parseOptions)

// WithDefaultDomain sets the registry domain to assume for an identifier that
// does not name one, in place of unikraft.io. An empty domain is ignored.
func WithDefaultDomain(domain string) ParseOpt {
	if domain == legacyDockerDomain {
		domain = dockerDomain
	}
	return func(o *parseOptions) {
		if domain != "" {
			o.defaultDomain = domain
		}
	}
}

func newParseOptions(opts []ParseOpt) parseOptions {
	o := parseOptions{defaultDomain: defaultDomain}
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
		return fromName(s, opts)
	}

	parsed, err := schemes.Parse(scheme)
	if err != nil {
		return Reference{}, err
	}

	switch {
	case parsed == schemes.OCI:
		return fromName(rest, opts)
	case parsed.IsHTTP():
		ref, err := fromHTTP(parsed, rest)
		if err != nil {
			return Reference{}, fmt.Errorf("%w %q: %w", ErrInvalidReference, s, err)
		}
		return ref, nil
	default:
		return Reference{}, fmt.Errorf("%w: %q %w", schemes.ErrUnsupported, scheme, ErrLocalScheme)
	}
}

// FromNamed returns a reference for an image that is already named, which is
// always an OCI registry reference.
func FromNamed(named distref.Named) (Reference, error) {
	if named == nil {
		return Reference{}, fmt.Errorf("%w: no name", ErrInvalidReference)
	}
	domain := distref.Domain(named)
	if !isDomain(domain) {
		return Reference{}, fmt.Errorf("%w: %q is not fully qualified", ErrInvalidReference, named.Name())
	}
	ref := Reference{
		scheme: schemes.OCI,
		domain: domain,
		path:   distref.Path(named),
	}
	if tagged, ok := named.(distref.Tagged); ok {
		ref.tag = tagged.Tag()
	}
	if digested, ok := named.(distref.Digested); ok {
		ref.digest = digested.Digest()
	}
	return ref, nil
}

func fromName(s string, opts []ParseOpt) (Reference, error) {
	named, err := ParseNormalizedNamed(s, opts...)
	if err != nil {
		return Reference{}, err
	}
	return FromNamed(named)
}

func fromHTTP(scheme schemes.Scheme, rest string) (Reference, error) {
	if strings.ContainsRune(rest, '%') {
		return Reference{}, errors.New("must not percent-encode characters")
	}

	if strings.ContainsRune(rest, '#') {
		return Reference{}, errors.New("must not have a fragment")
	}

	parsed, err := url.Parse(transport(scheme) + "://" + rest)
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

	named, err := distref.WithName(host + "/" + repository)
	if err != nil {
		return Reference{}, err
	}

	if identifier[0] == '@' {
		dgst, err := digest.Parse(identifier[1:])
		if err != nil {
			return Reference{}, err
		}
		if _, err := distref.WithDigest(named, dgst); err != nil {
			return Reference{}, err
		}
		ref.digest = dgst
		return ref, nil
	}
	if _, err := distref.WithTag(named, identifier); err != nil {
		return Reference{}, err
	}
	ref.tag = identifier
	return ref, nil
}

// isDomain reports whether s is shaped like a registry host rather than a
// repository path component.
func isDomain(s string) bool {
	return s == localhost || strings.ContainsAny(s, ".:") || strings.ToLower(s) != s
}

// normalizeHost lower-cases the authority and drops the transport's default
// port, so that two URIs naming the same host compare equal.
func normalizeHost(host string, scheme schemes.Scheme) (string, error) {
	if host == "" {
		return "", errors.New("missing host")
	}
	host = strings.ToLower(host)
	if !isDomain(host) {
		return "", fmt.Errorf("host %q is not a registry", host)
	}

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
	if scheme == schemes.HTTPSOCI {
		defaultPort = "443"
	}
	if port != defaultPort || !isDomain(name) {
		return host, nil
	}
	if strings.ContainsRune(name, ':') {
		// An IPv6 literal keeps its brackets.
		return "[" + name + "]", nil
	}
	return name, nil
}

// anchoredIdentifierRegexp matches a bare content identifier.
var anchoredIdentifierRegexp = regexp.MustCompile(`^(?:` + distref.IdentifierRegexp.String() + `)$`)

// ParseNormalizedNamed parses s into an OCI registry reference, completing the
// registry domain and repository prefix that it leaves implicit. Unlike Parse,
// it accepts no URI scheme.
func ParseNormalizedNamed(s string, opts ...ParseOpt) (distref.Named, error) {
	named, err := parseNormalizedNamed(s, newParseOptions(opts))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidReference, err)
	}
	return named, nil
}

func parseNormalizedNamed(s string, o parseOptions) (distref.Named, error) {
	if anchoredIdentifierRegexp.MatchString(s) {
		return nil, fmt.Errorf("invalid repository name (%s), cannot specify 64-byte hexadecimal strings", s)
	}

	domain, remainder := splitDockerDomain(s, o)
	var remote string
	if tagSep := strings.IndexRune(remainder, ':'); tagSep > -1 {
		remote = remainder[:tagSep]
	} else {
		remote = remainder
	}
	if strings.ToLower(remote) != remote {
		return nil, fmt.Errorf("invalid reference format: repository name (%s) must be lowercase", remote)
	}

	ref, err := distref.Parse(domain + "/" + remainder)
	if err != nil {
		return nil, err
	}
	named, isNamed := ref.(distref.Named)
	if !isNamed {
		return nil, fmt.Errorf("reference %s has no name", ref.String())
	}
	return named, nil
}

// splitDockerDomain splits a repository name into its domain and remote name,
// using the default domain when the name does not carry one.
func splitDockerDomain(name string, o parseOptions) (domain, remoteName string) {
	maybeDomain, maybeRemoteName, ok := strings.Cut(name, "/")
	switch {
	case !ok:
		domain, remoteName = o.defaultDomain, name
	case maybeDomain == legacyDockerDomain:
		domain, remoteName = dockerDomain, maybeRemoteName
	case isDomain(maybeDomain):
		domain, remoteName = maybeDomain, maybeRemoteName
	default:
		domain, remoteName = o.defaultDomain, name
	}

	if !strings.ContainsRune(remoteName, '/') {
		switch domain {
		case dockerDomain:
			remoteName = dockerPrefix + remoteName
		case o.defaultDomain:
			remoteName = defaultPrefix + remoteName
		}
	}

	return domain, remoteName
}
