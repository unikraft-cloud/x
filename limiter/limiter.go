// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package limiter provides a concurrency limiter for goroutines in Go.
//
// It allows you to control the maximum number of concurrent goroutines running
// at any time. The Limiter struct manages this using a buffered channel as a
// semaphore. You can start new goroutines with the Go method, which blocks if
// the limit is reached or if the limiter is closed. The Wait and Close methods
// ensure all running goroutines finish and prevent new ones from starting. This
// is useful for resource management and preventing overload in concurrent
// applications.
package limiter

import (
	"context"
	"sync"
)

// noCopy is used to ensure that we don't copy things that shouldn't be copied.
//
// See https://golang.org/issues/8005#issuecomment-190753527.
//
// Currently users of noCopy must use "//nolint:structcheck", because golint-ci
// does not handle this correctly.
type noCopy struct{}

func (noCopy) Lock() {}

// Limiter implements concurrent goroutine limiting.
//
// After calling Wait or Close, no new goroutines are allowed to start.
type Limiter struct {
	noCopy noCopy //nolint:structcheck

	limit  chan struct{}
	close  sync.Once
	closed chan struct{}
}

// NewLimiter creates a new limiter with limit set to n.
func NewLimiter(n int) *Limiter {
	return &Limiter{
		limit:  make(chan struct{}, n),
		closed: make(chan struct{}),
	}
}

// Go tries to start fn as a goroutine.  When the limit is reached it will wait
// until it can run it or the context is canceled.
func (limiter *Limiter) Go(ctx context.Context, fn func()) bool {
	if ctx.Err() != nil {
		return false
	}

	select {
	case limiter.limit <- struct{}{}:
	case <-limiter.closed:
		return false
	case <-ctx.Done():
		return false
	}

	go func() {
		defer func() { <-limiter.limit }()
		fn()
	}()

	return true
}

// Wait for all running goroutines to finish and disallows new goroutines to
// start.
func (limiter *Limiter) Wait() { limiter.Close() }

// Close waits for all running goroutines to finish and disallows new goroutines
// to start.
func (limiter *Limiter) Close() {
	limiter.close.Do(func() {
		close(limiter.closed)
		// ensure all goroutines are finished
		for i := 0; i < cap(limiter.limit); i++ {
			limiter.limit <- struct{}{}
		}
	})
}
