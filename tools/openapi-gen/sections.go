// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"bytes"
	"fmt"
	"strings"
)

type fileSection struct {
	name    string
	content []byte
}

func splitFileMarkers(content []byte) ([]byte, []fileSection, bool, error) {
	lines := bytes.Split(content, []byte("\n"))
	sections := []fileSection{}
	var preambleLines [][]byte

	var currentName string
	var currentLines [][]byte
	var sawMarker bool
	for _, line := range lines {
		if bytes.HasPrefix(line, []byte("--- ")) {
			sawMarker = true
			name := strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("--- "))))
			if name == "" {
				return nil, nil, true, fmt.Errorf("file marker missing variant")
			}
			if currentName != "" {
				sections = append(sections, fileSection{
					name:    currentName,
					content: bytes.Join(currentLines, []byte("\n")),
				})
			}
			currentName = name
			currentLines = nil
			continue
		}
		if !sawMarker {
			preambleLines = append(preambleLines, line)
			continue
		}
		currentLines = append(currentLines, line)
	}

	if !sawMarker {
		return nil, nil, false, nil
	}
	if currentName != "" {
		sections = append(sections, fileSection{
			name:    currentName,
			content: bytes.Join(currentLines, []byte("\n")),
		})
	}

	if len(sections) == 0 {
		return nil, nil, true, fmt.Errorf("file markers detected but no files found")
	}
	return bytes.Join(preambleLines, []byte("\n")), sections, true, nil
}
