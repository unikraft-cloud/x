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

// Signals is what [NotifyContext] registered: the signals that cancel its context.
type Signals struct {
	ch     chan os.Signal
	cancel context.CancelFunc
	once   sync.Once

	mu        sync.RWMutex
	suspended []os.Signal
}

// NotifyContext is [signal.NotifyContext] for a command that must survive a
// signal the rest of the CLI dies from: Suspend takes SIGINT for as long as the
// shell prompt is up and gives it back after, while SIGTERM cancels as usual.
func NotifyContext(parent context.Context, sig ...os.Signal) (context.Context, *Signals) {
	ctx, cancel := context.WithCancel(parent)

	s := &Signals{ch: make(chan os.Signal, len(sig)+1), cancel: cancel}
	signal.Notify(s.ch, sig...)

	go func() {
		for {
			select {
			case received := <-s.ch:
				if s.holds(received) {
					s.Stop()
					return
				}
			case <-ctx.Done():
				s.Stop()
				return
			}
		}
	}()

	return ctx, s
}

// Stop releases the registration and cancels the context; calling it again is a no-op.
func (s *Signals) Stop() {
	s.once.Do(func() { signal.Stop(s.ch) })
	s.cancel()
}

// Suspend lends sig to the caller until the returned restore is called.
func (s *Signals) Suspend(sig ...os.Signal) (restore func()) {
	s.mu.Lock()
	s.suspended = append(s.suspended, sig...)
	s.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()

			for _, sig := range sig {
				if i := slices.Index(s.suspended, sig); i >= 0 {
					s.suspended = slices.Delete(s.suspended, i, i+1)
				}
			}
		})
	}
}

func (s *Signals) holds(sig os.Signal) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !slices.Contains(s.suspended, sig)
}
