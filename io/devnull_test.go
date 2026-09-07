// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package io

import (
	stdio "io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDevNull(t *testing.T) {
	n, err := DevNull.Read(make([]byte, 8))
	assert.Zero(t, n)
	require.ErrorIs(t, err, stdio.EOF)

	n, err = DevNull.Write([]byte("gone"))
	require.NoError(t, err)
	assert.Equal(t, 4, n)

	assert.NoError(t, DevNull.Close())
}
