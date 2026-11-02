// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"errors"
	"fmt"
	"io"

	"unikraft.com/x/build/internal/buildfs"
)

// ErrTooLarge tells that a rootfs or a rom is larger than
// Builder.MaxRootfsSize.
var ErrTooLarge = buildfs.ErrTooLarge

// limitWriter writes to w until max bytes, then refuses each write with
// ErrTooLarge. A max of 0 is no limit.
type limitWriter struct {
	w    io.Writer
	max  int64
	n    int64
	over bool
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.max > 0 && l.n+int64(len(p)) > l.max {
		l.over = true
		return 0, l.tooLarge()
	}
	n, err := l.w.Write(p)
	l.n += int64(n)
	return n, err
}

// wrap returns err, with ErrTooLarge when a write went past the limit. A
// solve can lose the cause of a failed write on its way back.
func (l *limitWriter) wrap(err error) error {
	if err == nil || !l.over || errors.Is(err, ErrTooLarge) {
		return err
	}
	return fmt.Errorf("%w: %w", l.tooLarge(), err)
}

func (l *limitWriter) tooLarge() error {
	return fmt.Errorf("%w: more than %d bytes", ErrTooLarge, l.max)
}
