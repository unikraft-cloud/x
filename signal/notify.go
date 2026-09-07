// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package signal delivers process signals to a context, and lends one back to
// a command that needs it for itself.
package signal

import (
	"context"
	"os"
	"os/signal"
	"slices"
	"sync"
)

type controllerKey struct{}

// controller is the registration a context was built with, kept reachable so
// that a command needing a signal for itself can take it and give it back.
type controller struct {
	ch chan os.Signal

	mu        sync.RWMutex
	suspended []os.Signal
}

func (c *controller) holds(sig os.Signal) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !slices.Contains(c.suspended, sig)
}

// NotifyContext returns a copy of parent cancelled by the first of sig the
// context still holds, and a stop function that releases the registration.
func NotifyContext(parent context.Context, sig ...os.Signal) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)

	c := &controller{ch: make(chan os.Signal, len(sig)+1)}
	signal.Notify(c.ch, sig...)

	var once sync.Once
	stop := func() {
		once.Do(func() { signal.Stop(c.ch) })
		cancel()
	}

	ctx = context.WithValue(ctx, controllerKey{}, c)
	go func() {
		for {
			select {
			case received := <-c.ch:
				if c.holds(received) {
					stop()
					return
				}
			case <-ctx.Done():
				stop()
				return
			}
		}
	}()

	return ctx, stop
}

// Suspend lends sig to the caller until the returned restore is called.
func Suspend(ctx context.Context, sig ...os.Signal) (restore func()) {
	c, ok := ctx.Value(controllerKey{}).(*controller)
	if !ok {
		return func() {}
	}

	c.mu.Lock()
	c.suspended = append(c.suspended, sig...)
	c.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()

			for _, s := range sig {
				if i := slices.Index(c.suspended, s); i >= 0 {
					c.suspended = slices.Delete(c.suspended, i, i+1)
				}
			}
		})
	}
}
