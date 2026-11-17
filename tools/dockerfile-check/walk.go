// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// isDockerfile reports whether name follows a Dockerfile naming convention:
// Dockerfile, Dockerfile.<x> or <x>.Dockerfile, or the same for Containerfile.
func isDockerfile(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".dockerignore") {
		return false
	}
	for _, base := range []string{"Dockerfile", "Containerfile"} {
		// The prefix is case-sensitive, so that dockerfile.go isn't one.
		if name == base || strings.HasPrefix(name, base+".") ||
			strings.HasSuffix(lower, "."+strings.ToLower(base)) {
			return true
		}
	}
	return false
}

// collect returns the Dockerfiles in paths, sorted. A file named in paths is
// returned as is, and a directory is walked for files that isDockerfile.
func collect(paths []string, gitignored bool) ([]string, error) {
	var out []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			out = append(out, p)
			continue
		}

		var matcher gitignore.Matcher
		if gitignored {
			matcher, err = loadGitignore(p)
			if err != nil {
				return nil, err
			}
		}

		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(p, path)
			if err != nil {
				return err
			}
			if rel == "." {
				return nil
			}
			if d.IsDir() && d.Name() == ".git" {
				return filepath.SkipDir
			}
			if matcher != nil && matcher.Match(strings.Split(filepath.ToSlash(rel), "/"), d.IsDir()) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.IsDir() && isDockerfile(d.Name()) {
				out = append(out, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

func loadGitignore(root string) (gitignore.Matcher, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	patterns, err := gitignore.ReadPatterns(osfs.New(abs), nil)
	if err != nil {
		return nil, fmt.Errorf("reading gitignore patterns: %w", err)
	}
	return gitignore.NewMatcher(patterns), nil
}
