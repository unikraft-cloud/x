// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package io_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/stretchr/testify/assert"

	xio "unikraft.com/x/io"
)

func TestWithFdExposesTheUnderlyingFd(t *testing.T) {
	under := &colorprofile.Writer{Forward: os.Stdout, Profile: colorprofile.TrueColor}
	w := xio.WithFd(&bytes.Buffer{}, under)

	fd, ok := w.(interface{ Fd() uintptr })
	assert.True(t, ok, "the wrapper exposes Fd()")
	assert.Equal(t, os.Stdout.Fd(), fd.Fd(), "and forwards it past the colorprofile.Writer")
}

func TestWithFdIsUnchangedWithoutOne(t *testing.T) {
	w := xio.WithFd(&bytes.Buffer{}, &bytes.Buffer{})

	_, ok := w.(interface{ Fd() uintptr })
	assert.False(t, ok, "nothing underneath has an Fd, so none is invented")
}
