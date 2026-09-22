// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package uri parses the image references the tooling accepts.
package uri

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type URI struct {
	Scheme Scheme
	Path   string
}

func (u *URI) String() string {
	return fmt.Sprintf("%s://%s", u.Scheme, u.Path)
}

type Scheme string

const (
	SchemeOCI Scheme = "oci"

	SchemeOCILayout  Scheme = "oci-layout"
	SchemeOCIArchive Scheme = "oci-archive"
)

// Parse parses a URI of the form <scheme>://<path> and returns the parsed struct.
func Parse(src string) (*URI, error) {
	scheme, path, ok := strings.Cut(src, "://")
	if !ok {
		return nil, fmt.Errorf("invalid URI: %q", src)
	}
	return parseURI(scheme, path)
}

// ParseDefault attempts to parse the URI, and if it fails, returns a URI
// with the default scheme (OCI).
func ParseDefault(src string) (*URI, error) {
	if scheme, path, ok := strings.Cut(src, "://"); ok {
		return parseURI(scheme, path)
	}

	return &URI{
		Scheme: SchemeOCI,
		Path:   src,
	}, nil
}

// Guess is an opinionated parser that attempts to determine the URI scheme
// based on the input string.
//
// This function is intended to be used for user input, simplifying the
// experience by allowing the scheme to be inferred. However, avoid using it
// for parsing structured output, since you should be able to rely on more
// structured data.
func Guess(src string) (*URI, error) {
	if scheme, path, ok := strings.Cut(src, "://"); ok {
		return parseURI(scheme, path)
	}

	var stat os.FileInfo
	var statErr error
	if stat, statErr = os.Stat(src); statErr == nil {
		if stat.IsDir() {
			return &URI{
				Scheme: SchemeOCILayout,
				Path:   src,
			}, nil
		} else {
			return &URI{
				Scheme: SchemeOCIArchive,
				Path:   src,
			}, nil
		}
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}

	if path, tag := SplitPathTag(src); tag != "" {
		if stat, statErr = os.Stat(path); statErr == nil {
			if stat.IsDir() {
				return &URI{
					Scheme: SchemeOCILayout,
					Path:   src,
				}, nil
			} else {
				return &URI{
					Scheme: SchemeOCIArchive,
					Path:   src,
				}, nil
			}
		} else if !os.IsNotExist(statErr) {
			return nil, statErr
		}
	}

	if looksLikeTarball(src) {
		return &URI{
			Scheme: SchemeOCIArchive,
			Path:   src,
		}, nil
	}
	if looksLikeDir(src) {
		return &URI{
			Scheme: SchemeOCILayout,
			Path:   src,
		}, nil
	}

	if path, tag := SplitPathTag(src); tag != "" {
		if looksLikeTarball(path) {
			return &URI{
				Scheme: SchemeOCIArchive,
				Path:   src,
			}, nil
		}
		if looksLikeDir(path) {
			return &URI{
				Scheme: SchemeOCILayout,
				Path:   src,
			}, nil
		}
	}

	if looksLikePath(src) {
		return nil, fmt.Errorf("ambiguous path: %s", src)
	}

	return &URI{
		Scheme: SchemeOCI,
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

func parseScheme(scheme string) (Scheme, error) {
	switch Scheme(scheme) {
	case SchemeOCI, SchemeOCILayout, SchemeOCIArchive:
		return Scheme(scheme), nil
	default:
		return "", fmt.Errorf("unsupported URI scheme: %q", scheme)
	}
}

// SplitPathTag splits a path from the tag that follows its last colon.  The
// tag is empty when the path carries none.
func SplitPathTag(src string) (string, string) {
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
