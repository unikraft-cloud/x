// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import "unikraft.com/x/image-spec/uri"

type (
	URI       = uri.URI
	URIScheme = uri.Scheme
)

const (
	URISchemeOCI        = uri.SchemeOCI
	URISchemeOCILayout  = uri.SchemeOCILayout
	URISchemeOCIArchive = uri.SchemeOCIArchive
)

// ParseURI parses a URI of the form <scheme>://<path> and returns the parsed struct.
func ParseURI(src string) (*URI, error) { return uri.Parse(src) }

// ParseURIDefault attempts to parse the URI, and if it fails, returns a URI
// with the default scheme (OCI).
func ParseURIDefault(src string) (*URI, error) { return uri.ParseDefault(src) }

// GuessURI is an opinionated parser that attempts to determine the URI scheme
// based on the input string.
//
// This function is intended to be used for user input, simplifying the
// experience by allowing the scheme to be inferred. However, avoid using it
// for parsing structured output, since you should be able to rely on more
// structured data.
func GuessURI(src string) (*URI, error) { return uri.Guess(src) }
