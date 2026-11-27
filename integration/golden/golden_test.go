// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package golden_test

import (
	"strings"
	"testing"

	"unikraft.com/x/integration/golden"
)

func TestGild(t *testing.T) {
	t.Parallel()

	shout := func(_ *testing.T, s string) string { return strings.ToUpper(s) + "!\n" }

	t.Run("several arguments", func(t *testing.T) {
		t.Parallel()
		golden.Gild(t, shout, "first", "second", "third")
	})

	t.Run("one argument", func(t *testing.T) {
		t.Parallel()
		golden.Gild(t, shout, "only")
	})
}
