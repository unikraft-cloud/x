// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package io

import (
	"errors"
	stdio "io"
)

// Closers is a Closer that closes each of its closers in order, joining their
// errors.
type Closers []stdio.Closer

func (c Closers) Close() error {
	var err error
	for _, closer := range c {
		err = errors.Join(err, closer.Close())
	}
	return err
}
