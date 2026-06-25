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
	"sort"
	"strings"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

type fileFilter struct {
	Include     []string
	Exclude     []string
	NoGitignore bool
}

func (f *fileFilter) collect(paths []string) ([]string, error) {
	seen := map[string]struct{}{}
	var out []string

	add := func(p string) {
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		if _, ok := seen[abs]; ok {
			return
		}
		seen[abs] = struct{}{}
		out = append(out, p)
	}

	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", p, err)
		}

		if !info.IsDir() {
			if !f.match(p) {
				continue
			}
			add(p)
			continue
		}

		matcher, err := loadGitignoreMatcher(p, !f.NoGitignore)
		if err != nil {
			return nil, err
		}

		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, _ := filepath.Rel(p, path)
			parts := splitPath(rel)
			if d.IsDir() {
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				if matcher != nil && len(parts) > 0 && matcher.Match(parts, true) {
					return filepath.SkipDir
				}
				return nil
			}
			if matcher != nil && matcher.Match(parts, false) {
				return nil
			}
			if IsFragmentFile(d.Name()) {
				return nil
			}
			if !f.match(rel) {
				return nil
			}
			add(path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	sort.Strings(out)
	return out, nil
}

func (f *fileFilter) match(path string) bool {
	base := filepath.Base(path)
	if IsFragmentFile(base) {
		return false
	}
	if len(f.Include) > 0 {
		matched := false
		for _, g := range f.Include {
			if globMatch(g, path) || globMatch(g, base) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, g := range f.Exclude {
		if globMatch(g, path) || globMatch(g, base) {
			return false
		}
	}
	return true
}

func globMatch(pattern, name string) bool {
	ok, err := filepath.Match(pattern, name)
	if err != nil {
		return false
	}
	return ok
}

func splitPath(rel string) []string {
	rel = filepath.ToSlash(rel)
	if rel == "" || rel == "." {
		return nil
	}
	parts := strings.Split(rel, "/")
	out := parts[:0]
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		out = append(out, p)
	}
	return out
}

func loadGitignoreMatcher(root string, enabled bool) (gitignore.Matcher, error) {
	if !enabled {
		return nil, nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	fs := osfs.New(abs)
	patterns, err := gitignore.ReadPatterns(fs, nil)
	if err != nil {
		return nil, fmt.Errorf("reading gitignore patterns: %w", err)
	}
	if len(patterns) == 0 {
		return nil, nil
	}
	return gitignore.NewMatcher(patterns), nil
}
