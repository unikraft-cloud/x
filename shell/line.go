// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

func acceptMultiline(line []rune) bool {
	_, err := syntax.NewParser().Parse(strings.NewReader(string(line)), "")
	return !syntax.IsIncomplete(err)
}

func (s *state) isBuiltinName(name string) bool {
	_, ok := s.cfg.Builtins[name]
	return ok || slices.Contains(sessionBuiltinNames, name)
}
