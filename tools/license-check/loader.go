// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"fmt"
	"path/filepath"
	"regexp"
)

type CompiledFragment struct {
	Fragment *Fragment
	Regexps  []*regexp.Regexp
}

func (c *CompiledFragment) Match(data []byte) bool {
	for _, re := range c.Regexps {
		if re.Match(data) {
			return true
		}
	}
	return false
}

type FragmentLoader struct {
	dirToPath map[string]string // "" means "no fragment found"
	byPath    map[string]*CompiledFragment
}

func NewFragmentLoader() *FragmentLoader {
	return &FragmentLoader{
		dirToPath: map[string]string{},
		byPath:    map[string]*CompiledFragment{},
	}
}

func (l *FragmentLoader) Lookup(dir string) (*CompiledFragment, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	if path, ok := l.dirToPath[abs]; ok {
		if path == "" {
			return nil, nil
		}
		return l.byPath[path], nil
	}

	path, err := findFragmentPath(abs)
	if err != nil {
		return nil, err
	}
	l.dirToPath[abs] = path
	if path == "" {
		return nil, nil
	}

	if cf, ok := l.byPath[path]; ok {
		return cf, nil
	}
	frag, err := LoadFragmentFile(path)
	if err != nil {
		return nil, fmt.Errorf("loading fragment %s: %w", path, err)
	}
	res, err := frag.Compile()
	if err != nil {
		return nil, fmt.Errorf("compiling fragment %s: %w", path, err)
	}
	cf := &CompiledFragment{Fragment: frag, Regexps: res}
	l.byPath[path] = cf
	return cf, nil
}
