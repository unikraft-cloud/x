// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/stretchr/testify/require"
)

func TestBufGenerate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		bufDir        string
		generationDir string
	}{
		{
			name:          "simple",
			bufDir:        "testdata/simple/",
			generationDir: "dist/simple",
		},
		{
			name:          "multiple-definitions",
			bufDir:        "testdata/multiple-definitions/",
			generationDir: "dist/multiple-definitions",
		},
		{
			name:          "self-reference",
			bufDir:        "testdata/self-reference/",
			generationDir: "dist/self-reference",
		},
		{
			name:          "well-known-types",
			bufDir:        "testdata/well-known-types/",
			generationDir: "dist/well-known-types",
		},
		{
			name:          "tags",
			bufDir:        "testdata/tags/",
			generationDir: "dist/tags",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.NoError(t, os.RemoveAll(tt.generationDir))

			args := []string{
				"buf",
				"generate",
				tt.bufDir,
				"--template",
				filepath.Join(tt.bufDir, "buf.gen.yaml"),
			}
			cmd := exec.Command(
				args[0], args[1:]...,
			)
			out, err := cmd.CombinedOutput()
			require.NoErrorf(t, err, "buf generate %v failed:\n%s", args, out)

			typeCheck(t, tt.bufDir, tt.generationDir)
		})
	}
}

// typeCheck verifies that the generated code is sound golang.
func typeCheck(t *testing.T, bufDir, generationDir string) {
	t.Helper()

	absDir, err := filepath.Abs(generationDir)
	require.NoErrorf(t, err, "absolute path for %q", generationDir)

	cfg := &packages.Config{
		Context: t.Context(),
		Dir:     absDir,
		Mode:    packages.LoadSyntax,
	}

	pkgs, err := packages.Load(cfg, "./...")
	require.NoError(t, err)

	errs := []packages.Error{}
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			for _, e := range pkg.Errors {
				if containsAny(t, e.Error(), []string{"no required module provides package", "could not import"}) {
					// The generated code does not have a go.mod file; ignore package errors.
					continue
				}
				errs = append(errs, e)
			}
		}
	}

	require.Emptyf(t, errs, "typeCheck failed for %s (generated from %s)", generationDir, bufDir)
}

func containsAny(t *testing.T, s string, subs []string) bool {
	t.Helper()

	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
