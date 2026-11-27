// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package kraftfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

var ErrNoKraftfile = errors.New("no kraftfile found in directory")

func ParseFile(path string, opts ...ParseOpt) (*Kraftfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read file: %w", err)
	}
	return ParseBytes(data, opts...)
}

func ParseDirectory(path string, opts ...ParseOpt) (*Kraftfile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("could not read directory: %w", err)
	}
	// check if directory is a kraftfile itself
	if !info.IsDir() {
		return ParseFile(path, opts...)
	}
	for _, name := range DefaultFileNames {
		file := filepath.Join(path, name)
		info, err := os.Stat(file)
		if errors.Is(err, fs.ErrNotExist) || (err == nil && info.IsDir()) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("could not read file: %w", err)
		}
		return ParseFile(file, opts...)
	}
	return nil, ErrNoKraftfile
}

var DefaultFileNames = []string{
	"kraft.yaml",
	"kraft.yml",
	"Kraftfile.yml",
	"Kraftfile.yaml",
	"Kraftfile",
}
