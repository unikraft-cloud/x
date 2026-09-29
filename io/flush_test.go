// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package io_test

import (
	"bufio"
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	xio "unikraft.com/x/io"
)

func TestWriteFlusherFlushesBufferedWrites(t *testing.T) {
	var buf bytes.Buffer
	var wf xio.WriteFlusher = bufio.NewWriter(&buf)

	_, err := wf.Write([]byte("hello"))
	require.NoError(t, err)
	assert.Empty(t, buf.String(), "still buffered")

	require.NoError(t, wf.Flush())
	assert.Equal(t, "hello", buf.String())
}
