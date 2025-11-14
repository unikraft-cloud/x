// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if err := os.RemoveAll(tt.generationDir); err != nil {
				t.Fatalf("unable to remove directory: %v", err)
			}

			args := []string{
				"buf",
				"--config",
				filepath.Join(tt.bufDir, "buf.yaml"),
				"generate",
				"--template",
				filepath.Join(tt.bufDir, "buf.gen.yaml"),
			}
			cmd := exec.Command(
				args[0], args[1:]...,
			)
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("buf generate(%s) failed. error(%v). \nout(%s)", args, err, string(b))
			}

			typeCheck(t, tt.generationDir)
		})
	}
}

// typeCheck verifies that the generated code is sound golang code.
func typeCheck(t *testing.T, dir string) {
	t.Helper()

	absDir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("failed to get absolute path for %q: %v", dir, err)
	}

	cfg := &packages.Config{
		Context: t.Context(),
		Dir:     absDir,
		Mode:    packages.LoadSyntax,
	}

	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		t.Fatalf("Failed to load package: %v", err)
	}

	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			t.Logf("package(%s) has errors:", pkg.PkgPath)
			for _, e := range pkg.Errors {
				t.Log(e)
			}
			t.Fatalf("typeCheck failed for: %v", dir)
		}
	}
}
