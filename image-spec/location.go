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

	"unikraft.com/x/image-spec/schemes"
)

// Location is where an [Accessor] loads an image from, saves it to or deletes it
// at: a registry, a layout served over HTTP, a local OCI layout directory or a
// local OCI archive.
type Location struct {
	Scheme schemes.Scheme
	Path   string
}

func (l *Location) String() string {
	return fmt.Sprintf("%s://%s", l.Scheme, l.Path)
}

// ParseLocation parses a location of the form <scheme>://<path> and returns the parsed struct.
func ParseLocation(src string) (*Location, error) {
	scheme, path, ok := strings.Cut(src, "://")
	if !ok {
		return nil, fmt.Errorf("invalid location: %q", src)
	}
	return newLocation(scheme, path)
}

// ParseLocationDefault attempts to parse the location, and if it fails, returns a
// Location with the default scheme (OCI).
func ParseLocationDefault(src string) (*Location, error) {
	if scheme, path, ok := strings.Cut(src, "://"); ok {
		return newLocation(scheme, path)
	}

	return &Location{
		Scheme: schemes.OCI,
		Path:   src,
	}, nil
}

// GuessLocation is an opinionated parser that attempts to determine the URI scheme
// based on the input string.
//
// This function is intended to be used for user input, simplifying the
// experience by allowing the scheme to be inferred. However, avoid using it
// for parsing structured output, since you should be able to rely on more
// structured data.
func GuessLocation(src string) (*Location, error) {
	if scheme, path, ok := strings.Cut(src, "://"); ok {
		return newLocation(scheme, path)
	}

	var stat os.FileInfo
	var statErr error
	if stat, statErr = os.Stat(src); statErr == nil {
		if stat.IsDir() {
			return &Location{
				Scheme: schemes.OCILayout,
				Path:   src,
			}, nil
		} else {
			return &Location{
				Scheme: schemes.OCIArchive,
				Path:   src,
			}, nil
		}
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}

	if path, tag := splitPathTag(src); tag != "" {
		if stat, statErr = os.Stat(path); statErr == nil {
			if stat.IsDir() {
				return &Location{
					Scheme: schemes.OCILayout,
					Path:   src,
				}, nil
			} else {
				return &Location{
					Scheme: schemes.OCIArchive,
					Path:   src,
				}, nil
			}
		} else if !os.IsNotExist(statErr) {
			return nil, statErr
		}
	}

	if looksLikeTarball(src) {
		return &Location{
			Scheme: schemes.OCIArchive,
			Path:   src,
		}, nil
	}
	if looksLikeDir(src) {
		return &Location{
			Scheme: schemes.OCILayout,
			Path:   src,
		}, nil
	}

	if path, tag := splitPathTag(src); tag != "" {
		if looksLikeTarball(path) {
			return &Location{
				Scheme: schemes.OCIArchive,
				Path:   src,
			}, nil
		}
		if looksLikeDir(path) {
			return &Location{
				Scheme: schemes.OCILayout,
				Path:   src,
			}, nil
		}
	}

	if looksLikePath(src) {
		return nil, fmt.Errorf("ambiguous path: %s", src)
	}

	return &Location{
		Scheme: schemes.OCI,
		Path:   src,
	}, nil
}

func newLocation(scheme string, path string) (*Location, error) {
	uriScheme, err := schemes.Parse(scheme)
	if err != nil {
		return nil, err
	}
	return &Location{
		Scheme: uriScheme,
		Path:   path,
	}, nil
}

// splitPathTag splits a path from the tag that follows its last colon. The
// tag is empty when the path carries none. A volume name, as in "C:", is not a tag.
func splitPathTag(src string) (string, string) {
	vol := len(filepath.VolumeName(src))
	if idx := strings.LastIndex(src[vol:], ":"); idx >= 0 {
		return src[:vol+idx], src[vol+idx+1:]
	}
	return src, ""
}

func looksLikePath(s string) bool {
	return strings.HasPrefix(s, ".") || (s != "" && os.IsPathSeparator(s[0])) || filepath.VolumeName(s) != ""
}

func looksLikeDir(s string) bool {
	return s != "" && os.IsPathSeparator(s[len(s)-1])
}

func looksLikeTarball(s string) bool {
	parts := strings.Split(filepath.Base(s), ".")
	if len(parts) < 2 {
		return false
	}
	return slices.Contains(parts[1:], "tar")
}
