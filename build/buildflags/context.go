// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026, The buildx authors.
// Licensed under the Apache License, Version 2.0 (the "License").
// You may not use this file except in compliance with the License.

// original code imported from github.com/docker/buildx/util/buildflags/

package buildflags

import (
	"fmt"
	"strings"

	distref "github.com/distribution/reference"
)

// ParseContextNames parses named build contexts in the form name=value.
// Names are normalized using the same image-reference rules as BuildKit.
// Duplicate names after normalization are rejected.
func ParseContextNames(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}

	contexts := make(map[string]string, len(values))
	originalNames := make(map[string]string, len(values))

	for _, value := range values {
		if value == "" {
			continue
		}

		name, source, ok := strings.Cut(value, "=")
		if !ok || name == "" || source == "" {
			return nil, fmt.Errorf("invalid context value %q, expected name=value", value)
		}

		named, err := distref.ParseNormalizedNamed(name)
		if err != nil {
			return nil, fmt.Errorf("invalid context name %q: %w", name, err)
		}

		normalized := strings.TrimSuffix(distref.FamiliarString(named), ":latest")
		if normalized == "context" || normalized == "dockerfile" {
			return nil, fmt.Errorf("invalid context name %q", name)
		}

		if previous, exists := originalNames[normalized]; exists {
			return nil, fmt.Errorf(
				"duplicate context name %q (also specified as %q)",
				name,
				previous,
			)
		}

		originalNames[normalized] = name
		contexts[normalized] = source
	}

	return contexts, nil
}
