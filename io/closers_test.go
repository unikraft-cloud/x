// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package io

import (
	"errors"
	stdio "io"
	"testing"

	"github.com/stretchr/testify/require"
)

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

func TestClosersClosesInOrder(t *testing.T) {
	var order []int
	c := Closers{
		closerFunc(func() error { order = append(order, 1); return nil }),
		closerFunc(func() error { order = append(order, 2); return nil }),
	}

	require.NoError(t, c.Close())
	require.Equal(t, []int{1, 2}, order)
}

func TestClosersJoinsErrors(t *testing.T) {
	errA, errB := errors.New("a"), errors.New("b")
	var closed bool
	c := Closers{
		closerFunc(func() error { return errA }),
		closerFunc(func() error { closed = true; return nil }),
		closerFunc(func() error { return errB }),
	}

	err := c.Close()
	require.ErrorIs(t, err, errA)
	require.ErrorIs(t, err, errB)
	require.True(t, closed)
}

func TestClosersEmpty(t *testing.T) {
	var c stdio.Closer = Closers(nil)
	require.NoError(t, c.Close())
}
