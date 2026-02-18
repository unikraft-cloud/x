// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type URI struct {
	Scheme URIScheme
	Path   string
}

func (u *URI) String() string {
	return fmt.Sprintf("%s://%s", u.Scheme, u.Path)
}

type URIScheme string

const (
	URISchemeOCI URIScheme = "oci"

	URISchemeOCILayout  URIScheme = "oci-layout"
	URISchemeOCIArchive URIScheme = "oci-archive"
)

// ParseURI parses a URI of the form <scheme>://<path> and returns the parsed struct.
func ParseURI(src string) (*URI, error) {
	scheme, path, ok := strings.Cut(src, "://")
	if !ok {
		return nil, fmt.Errorf("invalid URI: %q", src)
	}
	return parseURI(scheme, path)
}

// ParseURIDefault attempts to parse the URI, and if it fails, returns a URI
// with the default scheme (OCI).
func ParseURIDefault(src string) (*URI, error) {
	if scheme, path, ok := strings.Cut(src, "://"); ok {
		return parseURI(scheme, path)
	}

	return &URI{
		Scheme: URISchemeOCI,
		Path:   src,
	}, nil
}

// GuessURI is an opinionated parser that attempts to determine the URI scheme
// based on the input string.
//
// This function is intended to be used for user input, simplifying the
// experience by allowing the schema to be inferred. However, avoid using it
// for parsing structured output, since you should be able to rely on more
// structured data.
func GuessURI(src string) (*URI, error) {
	if scheme, path, ok := strings.Cut(src, "://"); ok {
		return parseURI(scheme, path)
	}

	var stat os.FileInfo
	var statErr error
	if stat, statErr = os.Stat(src); statErr == nil {
		if stat.IsDir() {
			return &URI{
				Scheme: URISchemeOCILayout,
				Path:   src,
			}, nil
		} else {
			return &URI{
				Scheme: URISchemeOCIArchive,
				Path:   src,
			}, nil
		}
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}

	if path, tag := parsePathTag(src); tag != "" {
		if stat, statErr = os.Stat(path); statErr == nil {
			if stat.IsDir() {
				return &URI{
					Scheme: URISchemeOCILayout,
					Path:   src,
				}, nil
			} else {
				return &URI{
					Scheme: URISchemeOCIArchive,
					Path:   src,
				}, nil
			}
		} else if !os.IsNotExist(statErr) {
			return nil, statErr
		}
	}

	if looksLikeTarball(src) {
		return &URI{
			Scheme: URISchemeOCIArchive,
			Path:   src,
		}, nil
	}
	if looksLikeDir(src) {
		return &URI{
			Scheme: URISchemeOCILayout,
			Path:   src,
		}, nil
	}

	if path, tag := parsePathTag(src); tag != "" {
		if looksLikeTarball(path) {
			return &URI{
				Scheme: URISchemeOCIArchive,
				Path:   src,
			}, nil
		}
		if looksLikeDir(path) {
			return &URI{
				Scheme: URISchemeOCILayout,
				Path:   src,
			}, nil
		}
	}

	if looksLikePath(src) {
		return nil, fmt.Errorf("ambiguous path: %s", src)
	}

	return &URI{
		Scheme: URISchemeOCI,
		Path:   src,
	}, nil
}

func parseURI(scheme string, path string) (*URI, error) {
	uriScheme, err := parseScheme(scheme)
	if err != nil {
		return nil, err
	}
	return &URI{
		Scheme: uriScheme,
		Path:   path,
	}, nil
}

func parseScheme(scheme string) (URIScheme, error) {
	switch URIScheme(scheme) {
	case URISchemeOCI, URISchemeOCILayout, URISchemeOCIArchive:
		return URIScheme(scheme), nil
	default:
		return "", fmt.Errorf("unsupported URI scheme: %q", scheme)
	}
}

func parsePathTag(src string) (string, string) {
	if idx := strings.LastIndex(src, ":"); idx >= 0 {
		return src[:idx], src[idx+1:]
	}
	return src, ""
}

func looksLikePath(s string) bool {
	return strings.HasPrefix(s, ".") || strings.HasPrefix(s, string(os.PathSeparator))
}

func looksLikeDir(s string) bool {
	return strings.HasSuffix(s, string(os.PathSeparator))
}

func looksLikeTarball(s string) bool {
	parts := strings.Split(filepath.Base(s), ".")
	if len(parts) < 2 {
		return false
	}
	return slices.Contains(parts[1:], "tar")
}
