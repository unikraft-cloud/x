// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func match(t *testing.T, fragment, body string) bool {
	t.Helper()
	f, err := LoadFragmentString(fragment)
	require.NoError(t, err)
	res, err := f.Compile()
	require.NoError(t, err)
	for _, re := range res {
		if re.MatchString(body) {
			return true
		}
	}
	return false
}

func TestFragmentLiteral(t *testing.T) {
	const frag = `SPDX-License-Identifier: BSD-3-Clause
Copyright (c) 2026, ACME Corp.`

	// Comment-style agnostic: both // and # prefixed forms must match.
	assert.True(t, match(t, frag,
		"// SPDX-License-Identifier: BSD-3-Clause\n// Copyright (c) 2026, ACME Corp.\npackage x\n"))
	assert.True(t, match(t, frag,
		"# SPDX-License-Identifier: BSD-3-Clause\n# Copyright (c) 2026, ACME Corp.\n"))

	// A body that has only one of the required lines must fail.
	assert.False(t, match(t, frag,
		"// SPDX-License-Identifier: BSD-3-Clause\npackage x\n"))
}

func TestFragmentRegexPlaceholder(t *testing.T) {
	const frag = `Copyright (c) <<\d{4}(?:-\d{4})?>>, ACME.`

	assert.True(t, match(t, frag, "// Copyright (c) 2026, ACME.\n"))
	assert.True(t, match(t, frag, "// Copyright (c) 2024-2026, ACME.\n"))
	assert.False(t, match(t, frag, "// Copyright (c) twentysix, ACME.\n"))
}

func TestFragmentAlternatives(t *testing.T) {
	const frag = `SPDX-License-Identifier: Apache-2.0
Copyright (c) <<\d{4}>>, The containerd Authors.
---
SPDX-License-Identifier: BSD-3-Clause
Copyright (c) <<\d{4}>>, ACME.`

	assert.True(t, match(t, frag,
		"// SPDX-License-Identifier: Apache-2.0\n// Copyright (c) 2026, The containerd Authors.\n"),
		"first alternative should match")
	assert.True(t, match(t, frag,
		"// SPDX-License-Identifier: BSD-3-Clause\n// Copyright (c) 2026, ACME.\n"),
		"second alternative should match")
	assert.False(t, match(t, frag,
		"// SPDX-License-Identifier: MIT\n// Copyright (c) 2026, Someone Else.\n"),
		"unrelated license should not match")
}

func TestFindFragmentParentWalk(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, fstest.Apply(
		fstest.CreateFile("/LICENSE.fragment.txt", []byte("Copyright (c) <<\\d{4}>>, ROOT.\n"), 0o644),
		fstest.CreateDir("/sub", 0o755),
		fstest.CreateDir("/sub/deep", 0o755),
	).Apply(root))

	frag, err := FindFragment(filepath.Join(root, "sub", "deep"))
	require.NoError(t, err)
	require.NotNil(t, frag, "expected to find fragment by walking up")
	assert.True(t, strings.HasSuffix(frag.Source, "LICENSE.fragment.txt"),
		"unexpected fragment source: %s", frag.Source)
	require.Len(t, frag.Alternatives, 1)
	require.Len(t, frag.Alternatives[0], 1)
}

func TestCollectHonorsGitignore(t *testing.T) {
	root := t.TempDir()

	goodFile := []byte("// Copyright (c) 2026, ACME.\npackage x\n")
	badFile := []byte("package x\n")

	require.NoError(t, fstest.Apply(
		fstest.CreateFile("/LICENSE.fragment.txt", []byte("Copyright (c) <<\\d{4}>>, ACME.\n"), 0o644),
		fstest.CreateFile("/.gitignore", []byte("ignored/\nnested-ignored.go\n"), 0o644),
		fstest.CreateFile("/keep.go", goodFile, 0o644),
		fstest.CreateFile("/bad.go", badFile, 0o644),
		fstest.CreateDir("/sub", 0o755),
		fstest.CreateFile("/sub/.gitignore", []byte("local-ignored.go\n"), 0o644),
		fstest.CreateFile("/sub/keep.go", goodFile, 0o644),
		fstest.CreateFile("/sub/local-ignored.go", badFile, 0o644),
		fstest.CreateFile("/sub/nested-ignored.go", badFile, 0o644),
		fstest.CreateDir("/ignored", 0o755),
		fstest.CreateFile("/ignored/wat.go", badFile, 0o644),
	).Apply(root))

	filter := &fileFilter{Include: []string{"*.go"}}
	files, err := filter.collect([]string{root})
	require.NoError(t, err)

	// Make paths relative for stable comparison.
	got := make([]string, 0, len(files))
	for _, f := range files {
		rel, err := filepath.Rel(root, f)
		require.NoError(t, err)
		got = append(got, filepath.ToSlash(rel))
	}
	assert.ElementsMatch(t, []string{"bad.go", "keep.go", "sub/keep.go"}, got)

	// And the per-file fragment check: keep.go (both) pass, bad.go fails.
	frag, err := FindFragment(root)
	require.NoError(t, err)
	require.NotNil(t, frag)
	res, err := frag.Compile()
	require.NoError(t, err)

	results := map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		require.NoError(t, err)
		matched := false
		for _, re := range res {
			if re.Match(data) {
				matched = true
				break
			}
		}
		rel, _ := filepath.Rel(root, f)
		results[filepath.ToSlash(rel)] = matched
	}
	assert.Equal(t, map[string]bool{
		"bad.go":      false,
		"keep.go":     true,
		"sub/keep.go": true,
	}, results)
}
