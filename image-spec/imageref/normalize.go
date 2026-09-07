// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imageref

import (
	"fmt"
	"regexp"
	"strings"

	ociref "github.com/distribution/reference"
)

// Docker Hub is named explicitly rather than going through parseOptions: its
// implicit namespace is always "library/" and "index.docker.io" is its alias,
// both of which are part of the reference grammar rather than the caller's
// choice. These are also the zero-option defaults.
const (
	dockerDomain       = "docker.io"
	legacyDockerDomain = "index.docker.io"

	// officialRepoPrefix is the namespace Docker Hub serves its own images
	// from.
	officialRepoPrefix = "library/"

	// defaultTag is the tag an identifier naming neither tag nor digest means.
	defaultTag = "latest"

	// localhost is a reserved namespace and always considered a domain.
	localhost = "localhost"
)

// anchoredIdentifierRegexp matches a bare content identifier.
var anchoredIdentifierRegexp = regexp.MustCompile(`^(?:` + ociref.IdentifierRegexp.String() + `)$`)

// parseNormalizedNamed parses s into a named reference, completing the registry
// domain and repository prefix.
func parseNormalizedNamed(s string, o parseOptions) (ociref.Named, error) {
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

	ref, err := ociref.Parse(domain + "/" + remainder)
	if err != nil {
		return nil, err
	}
	named, isNamed := ref.(ociref.Named)
	if !isNamed {
		return nil, fmt.Errorf("reference %s has no name", ref.String())
	}
	return named, nil
}

// splitDockerDomain splits a repository name into its domain and remote name,
// using the default domain when the name does not carry one.
func splitDockerDomain(name string, o parseOptions) (domain, remoteName string) {
	maybeDomain, maybeRemoteName, ok := strings.Cut(name, "/")
	if !ok {
		// Fast-path for single element
		return o.defaultDomain, o.defaultPrefix + name
	}

	switch {
	case maybeDomain == legacyDockerDomain:
		// Canonicalize the legacy "Docker Index" domain.
		domain, remoteName = dockerDomain, maybeRemoteName
	case maybeDomain == localhost ||
		strings.ContainsAny(maybeDomain, ".:") ||
		strings.ToLower(maybeDomain) != maybeDomain:
		// Shaped like a registry host rather than a repository segment.
		domain, remoteName = maybeDomain, maybeRemoteName
	default:
		// Not a domain, so use the default and take the whole input as the name.
		domain, remoteName = o.defaultDomain, name
	}

	if !strings.ContainsRune(remoteName, '/') {
		switch domain {
		case o.defaultDomain:
			// "unikraft.io/nginx[:tag]" => "unikraft.io/official/nginx[:tag]".
			remoteName = o.defaultPrefix + remoteName
		case dockerDomain:
			// Docker Hub's official namespace is fixed.
			remoteName = officialRepoPrefix + remoteName
		}
	}

	return domain, remoteName
}
