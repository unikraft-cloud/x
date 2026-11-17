// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package progress provides progress tracking for content store operations.
//
// The package uses a context-based pattern similar to logging, allowing
// progress tracking to be transparently added by wrapping content stores.
//
// Example usage:
//
//	tracker := &myTracker{}
//	ctx = progress.WithTracker(ctx, tracker)
//	store = progress.WrapIngester(store)
//	// Now all writes to store will report progress via tracker
package progress

import (
	"context"
	"sync"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/time/rate"
)

// Action describes what operation is being performed during a content transfer.
type Action string

const (
	// ActionDownloading indicates content is being downloaded.
	ActionDownloading Action = "downloading"
	// ActionUploading indicates content is being uploaded.
	ActionUploading Action = "uploading"
)

// Progress represents a progress update for a content operation.
type Progress struct {
	// Descriptor is the OCI descriptor of the content being transferred.
	Descriptor *ocispec.Descriptor

	// Action describes what operation is being performed.
	Action Action

	// Current is the number of bytes transferred so far.
	Current int64

	// Total is the total number of bytes to transfer (from Descriptor.Size).
	Total int64

	// Started is when the operation began.
	Started time.Time

	// Completed is when the operation finished. A zero value indicates the
	// operation is still in progress.
	Completed time.Time
}

// Tracker receives progress updates for content operations.
type Tracker interface {
	// Update is called with progress information during content operations.
	// It may be called multiple times for the same descriptor as data is transferred.
	// When the operation completes, Update is called one final time with Completed
	// set to a non-zero time.
	Update(Progress)
}

// contextKey is the type used for storing the tracker in context.
type contextKey struct{}

// WithTracker returns a new context with the given tracker attached.
func WithTracker(ctx context.Context, t Tracker) context.Context {
	return context.WithValue(ctx, contextKey{}, t)
}

// FromContext returns the Tracker from the context, if present.
func FromContext(ctx context.Context) (Tracker, bool) {
	t, ok := ctx.Value(contextKey{}).(Tracker)
	return t, ok
}

// G returns the Tracker from the context, or a no-op tracker if not present.
// This mirrors the log.G pattern for convenient access.
func G(ctx context.Context) Tracker {
	if t, ok := FromContext(ctx); ok {
		return t
	}
	return nopTracker{}
}

// nopTracker is a no-op implementation of Tracker.
type nopTracker struct{}

func (nopTracker) Update(Progress) {}

// TrackerFunc is an adapter to allow ordinary functions to be used as Trackers.
type TrackerFunc func(Progress)

// Update implements Tracker.
func (f TrackerFunc) Update(p Progress) {
	f(p)
}

// WithLimiter wraps a Tracker so updates are rate-limited.
//
// The wrapped tracker always receives the first and last (Completed) update for
// each transfer operation, while intermediate updates are propagated only when
// the limiter allows.
func WithLimiter(t Tracker, limiter *rate.Limiter) Tracker {
	if t == nil {
		return nopTracker{}
	}
	if limiter == nil {
		return t
	}

	return &limitedTracker{
		tracker: t,
		limiter: limiter,
		seen:    make(map[progressKey]struct{}),
	}
}

type limitedTracker struct {
	tracker Tracker
	limiter *rate.Limiter

	mu   sync.Mutex
	seen map[progressKey]struct{}
}

type progressKey struct {
	action  Action
	digest  string
	total   int64
	started time.Time
}

func (l *limitedTracker) Update(p Progress) {
	key := progressKeyFrom(p)
	isLast := !p.Completed.IsZero()

	l.mu.Lock()
	_, seen := l.seen[key]
	if !seen {
		l.seen[key] = struct{}{}
	}
	if isLast {
		delete(l.seen, key)
	}
	l.mu.Unlock()

	// Always emit first and final updates; rate-limit only intermediate updates.
	if !seen || isLast || l.limiter.Allow() {
		l.tracker.Update(p)
	}
}

func progressKeyFrom(p Progress) progressKey {
	key := progressKey{
		action:  p.Action,
		total:   p.Total,
		started: p.Started,
	}
	if p.Descriptor != nil {
		key.digest = p.Descriptor.Digest.String()
		if key.total == 0 {
			key.total = p.Descriptor.Size
		}
	}
	return key
}
