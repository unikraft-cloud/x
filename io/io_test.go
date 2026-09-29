// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package io

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNopWriteCloserWritesThrough(t *testing.T) {
	var buf bytes.Buffer
	wc := NopWriteCloser(&buf)

	n, err := wc.Write([]byte("hello"))
	require.NoError(t, err)
	require.Equal(t, len("hello"), n)
	require.Equal(t, "hello", buf.String())
}

func TestNopWriteCloserCloseDoesNotClose(t *testing.T) {
	w := &closeCounter{}
	wc := NopWriteCloser(w)

	require.NoError(t, wc.Close())
	// Closing again must stay a no-op, and must never reach the wrapped
	// writer.
	require.NoError(t, wc.Close())
	require.Zero(t, w.closes, "wrapped writer must never be closed")
}

// closeCounter records Close calls so a leaked close is visible.
type closeCounter struct {
	closes int
}

func (c *closeCounter) Write(p []byte) (int, error) { return len(p), nil }

func (c *closeCounter) Close() error {
	c.closes++
	return errors.New("wrapped writer must not be closed")
}
