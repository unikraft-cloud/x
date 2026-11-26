// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"slices"
	"strings"

	"github.com/distribution/reference"

	"unikraft.com/x/dockerfile"
)

// unpinned returns the images that dt builds from without both a tag and a
// digest, other than those that start with a prefix in allow.
func unpinned(dt []byte, allow []string) ([]string, error) {
	images, err := dockerfile.Images(dt)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, image := range images {
		if slices.ContainsFunc(allow, func(prefix string) bool {
			return strings.HasPrefix(image, prefix)
		}) {
			continue
		}
		ref, err := reference.ParseNormalizedNamed(image)
		if err != nil {
			return nil, err
		}
		_, tagged := ref.(reference.Tagged)
		_, digested := ref.(reference.Digested)
		if !tagged || !digested {
			out = append(out, image)
		}
	}
	return out, nil
}
