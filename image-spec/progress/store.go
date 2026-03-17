// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package progress

import (
	"context"
	"io"
	"sync/atomic"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// WrapIngester wraps a content.Ingester to report write progress via the
// Tracker stored in the context.
func WrapIngester(store content.Ingester) content.Ingester {
	return &trackingIngester{store: store}
}

// WrapProvider wraps a content.Provider to report read progress via the
// Tracker stored in the context.
func WrapProvider(store content.Provider) content.Provider {
	return &trackingProvider{store: store}
}

// trackingIngester wraps content.Ingester with progress tracking.
type trackingIngester struct {
	store content.Ingester
}

func (t *trackingIngester) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	// Extract descriptor info from writer options
	var wOpts content.WriterOpts
	for _, opt := range opts {
		if err := opt(&wOpts); err != nil {
			return nil, err
		}
	}

	w, err := t.store.Writer(ctx, opts...)
	if err != nil {
		return nil, err
	}

	tracker, ok := FromContext(ctx)
	if !ok {
		return w, nil
	}

	started := time.Now()
	tracker.Update(Progress{
		Descriptor: &wOpts.Desc,
		Action:     ActionUploading,
		Current:    0,
		Total:      wOpts.Desc.Size,
		Started:    started,
	})

	return &trackingWriter{
		Writer:  w,
		tracker: tracker,
		desc:    &wOpts.Desc,
		started: started,
	}, nil
}

// trackingProvider wraps content.Provider with progress tracking.
type trackingProvider struct {
	store content.Provider
}

func (t *trackingProvider) ReaderAt(ctx context.Context, desc ocispec.Descriptor) (content.ReaderAt, error) {
	ra, err := t.store.ReaderAt(ctx, desc)
	if err != nil {
		return nil, err
	}

	tracker, ok := FromContext(ctx)
	if !ok {
		// No tracker in context, return unwrapped reader
		return ra, nil
	}

	started := time.Now()

	// Send initial progress update
	tracker.Update(Progress{
		Descriptor: &desc,
		Action:     ActionDownloading,
		Current:    0,
		Total:      desc.Size,
		Started:    started,
	})

	return &trackingReaderAt{
		ReaderAt: ra,
		tracker:  tracker,
		desc:     &desc,
		started:  started,
	}, nil
}

// trackingWriter wraps content.Writer with progress tracking.
type trackingWriter struct {
	content.Writer
	tracker Tracker
	desc    *ocispec.Descriptor
	current atomic.Int64
	started time.Time
}

func (w *trackingWriter) Write(p []byte) (n int, err error) {
	n, err = w.Writer.Write(p)
	w.current.Add(int64(n))

	w.tracker.Update(Progress{
		Descriptor: w.desc,
		Action:     ActionUploading,
		Current:    w.current.Load(),
		Total:      w.desc.Size,
		Started:    w.started,
	})
	return n, err
}

func (w *trackingWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	err := w.Writer.Commit(ctx, size, expected, opts...)

	// Send final progress update.
	p := Progress{
		Descriptor: w.desc,
		Action:     ActionUploading,
		Current:    w.current.Load(),
		Total:      w.desc.Size,
		Started:    w.started,
		Completed:  time.Now(),
	}
	w.tracker.Update(p)
	return err
}

func (w *trackingWriter) Close() error {
	return w.Writer.Close()
}

// trackingReaderAt wraps content.ReaderAt with progress tracking.
// It is safe for concurrent use, matching the io.ReaderAt contract.
type trackingReaderAt struct {
	content.ReaderAt
	tracker Tracker
	desc    *ocispec.Descriptor
	current atomic.Int64
	started time.Time
	done    atomic.Bool
}

func (r *trackingReaderAt) ReadAt(p []byte, off int64) (n int, err error) {
	n, err = r.ReaderAt.ReadAt(p, off)

	current := r.current.Add(int64(n))
	current = min(current, r.desc.Size)

	prog := Progress{
		Descriptor: r.desc,
		Action:     ActionDownloading,
		Current:    current,
		Total:      r.desc.Size,
		Started:    r.started,
	}
	if err == io.EOF {
		if r.done.CompareAndSwap(false, true) {
			prog.Completed = time.Now()
			r.tracker.Update(prog)
		}
	} else {
		r.tracker.Update(prog)
	}
	return n, err
}

func (r *trackingReaderAt) Close() error {
	// Send final progress update if we haven't already
	if r.done.CompareAndSwap(false, true) {
		r.tracker.Update(Progress{
			Descriptor: r.desc,
			Action:     ActionDownloading,
			Current:    min(r.current.Load(), r.desc.Size),
			Total:      r.desc.Size,
			Started:    r.started,
			Completed:  time.Now(),
		})
	}
	return r.ReaderAt.Close()
}
