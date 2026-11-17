// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package generator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunTemplates(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "out.txt.tmpl"), []byte("dir"), 0o644))
	packs := map[string]*Pack{"pack": {FS: tmpls("out.txt.tmpl", "pack")}}

	for templates, want := range map[string]string{
		"pack": "pack",
		dir:    "dir",
	} {
		out := t.TempDir()
		require.NoError(t, Run(Options{
			Input:     writeSpec(t),
			Output:    out,
			Templates: templates,
			Packs:     packs,
		}))
		got, err := os.ReadFile(filepath.Join(out, "out.gen.txt"))
		require.NoError(t, err)
		assert.Equal(t, want, string(got), templates)
	}
}
