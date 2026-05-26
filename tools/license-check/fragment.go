// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var FragmentNames = []string{
	"license.fragment.txt",
	"license.fragment.md",
	"license-fragment.txt",
	"license-fragment.md",
	"license.fragment",
}

func IsFragmentFile(name string) bool {
	return slices.Contains(FragmentNames, strings.ToLower(name))
}

// A fragment may contain multiple alternatives separated by `---`; a file
// matches if any alternative matches.
type Fragment struct {
	Source       string
	Alternatives [][]string
}

var (
	regexPlaceholder  = regexp.MustCompile(`<<(.*?)>>`)
	fragmentSeparator = regexp.MustCompile(`^-{3,}\s*$`)
)

func LoadFragmentString(content string) (*Fragment, error) {
	var alts [][]string
	current := []string{}
	for raw := range strings.SplitSeq(content, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if fragmentSeparator.MatchString(trimmed) {
			if len(current) > 0 {
				alts = append(alts, current)
				current = nil
			}
			continue
		}
		if trimmed == "" {
			continue
		}
		current = append(current, trimmed)
	}
	if len(current) > 0 {
		alts = append(alts, current)
	}
	if len(alts) == 0 {
		return nil, fmt.Errorf("fragment is empty")
	}
	return &Fragment{Alternatives: alts}, nil
}

func LoadFragmentFile(path string) (*Fragment, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, err := LoadFragmentString(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	f.Source = path
	return f, nil
}

func (f *Fragment) Compile() ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(f.Alternatives))
	for i, lines := range f.Alternatives {
		re, err := compileAlternative(lines)
		if err != nil {
			where := ""
			if f.Source != "" {
				where = f.Source + " "
			}
			return nil, fmt.Errorf("%s(alternative %d): %w", where, i+1, err)
		}
		out = append(out, re)
	}
	return out, nil
}

func compileAlternative(lines []string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("(?m)")
	for i, line := range lines {
		if i > 0 {
			b.WriteString(`.*\n(?:.*\n)*?`)
		}
		b.WriteString(`.*`)
		b.WriteString(compileLinePattern(line))
		b.WriteString(`.*`)
	}
	return regexp.Compile(b.String())
}

func compileLinePattern(line string) string {
	if !strings.Contains(line, "<<") {
		return regexp.QuoteMeta(line)
	}
	var b strings.Builder
	idx := 0
	for _, m := range regexPlaceholder.FindAllStringSubmatchIndex(line, -1) {
		start, end := m[0], m[1]
		bodyStart, bodyEnd := m[2], m[3]
		b.WriteString(regexp.QuoteMeta(line[idx:start]))
		b.WriteString("(?:")
		b.WriteString(line[bodyStart:bodyEnd])
		b.WriteString(")")
		idx = end
	}
	b.WriteString(regexp.QuoteMeta(line[idx:]))
	return b.String()
}

func FindFragment(dir string) (*Fragment, error) {
	path, err := findFragmentPath(dir)
	if err != nil || path == "" {
		return nil, err
	}
	return LoadFragmentFile(path)
}

func findFragmentPath(dir string) (string, error) {
	d, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		entries, err := os.ReadDir(d)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				if IsFragmentFile(e.Name()) {
					return filepath.Join(d, e.Name()), nil
				}
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", nil
		}
		d = parent
	}
}
