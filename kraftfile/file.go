// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package kraftfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

func ParseFile(path string, opts ...ParseOpt) (*Kraftfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read file: %w", err)
	}
	return ParseBytes(data, opts...)
}

func ParseDirectory(path string, opts ...ParseOpt) (*Kraftfile, error) {
	files, err := os.ReadDir(path)
	// check if directory is a kraftfile itself
	if errors.Is(err, syscall.ENOTDIR) {
		return ParseFile(path, opts...)
	} else if err != nil {
		return nil, fmt.Errorf("could not read directory: %w", err)
	}
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		if slices.Contains(DefaultFileNames, file.Name()) {
			return ParseFile(filepath.Join(path, file.Name()), opts...)
		}
	}
	return nil, fmt.Errorf("no kraftfile found in directory")
}

var DefaultFileNames = []string{
	"kraft.yaml",
	"kraft.yml",
	"Kraftfile.yml",
	"Kraftfile.yaml",
	"Kraftfile",
}
