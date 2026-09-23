// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !js

package io_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/charmbracelet/colorprofile"

	xio "unikraft.com/x/io"
)

func TestIsStdin(t *testing.T) {
	t.Parallel()

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()

	if !xio.IsStdin(os.Stdin) {
		t.Error("IsStdin(os.Stdin) = false, want true")
	}
	if xio.IsStdin(pr) {
		t.Error("IsStdin(pipe) = true, want false")
	}
	if xio.IsStdin(&bytes.Buffer{}) {
		t.Error("IsStdin(*bytes.Buffer) = true, want false")
	}
}

func TestIsStdout(t *testing.T) {
	t.Parallel()

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()

	if !xio.IsStdout(os.Stdout) {
		t.Error("IsStdout(os.Stdout) = false, want true")
	}
	if !xio.IsStdout(&colorprofile.Writer{Forward: os.Stdout}) {
		t.Error("IsStdout(colorprofile wrapping os.Stdout) = false, want true")
	}
	if xio.IsStdout(pw) {
		t.Error("IsStdout(pipe) = true, want false")
	}
	if xio.IsStdout(&bytes.Buffer{}) {
		t.Error("IsStdout(*bytes.Buffer) = true, want false")
	}
}
