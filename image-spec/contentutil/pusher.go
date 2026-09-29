// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026, The BuildKit Authors.
// Licensed under the Apache License, Version 2.0 (the "License").
// You may not use this file except in compliance with the License.

package contentutil

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
)

// FromPusher mirrors buildkit's contentutil.FromPusher.
// Source: github.com/moby/buildkit/util/contentutil/pusher.go
func FromPusher(p remotes.Pusher) content.Ingester {
	var mu sync.Mutex
	c := sync.NewCond(&mu)
	return &pushingIngester{
		mu:     &mu,
		c:      c,
		p:      p,
		active: map[digest.Digest]struct{}{},
	}
}

type pushingIngester struct {
	p remotes.Pusher

	mu     *sync.Mutex
	c      *sync.Cond
	active map[digest.Digest]struct{}
}

// Writer implements content.Ingester. desc.MediaType must be set for manifest blobs.
func (i *pushingIngester) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	var wOpts content.WriterOpts
	for _, opt := range opts {
		if err := opt(&wOpts); err != nil {
			return nil, err
		}
	}
	if wOpts.Ref == "" {
		return nil, fmt.Errorf("ref must not be empty: %w", errdefs.ErrInvalidArgument)
	}

	st := time.Now()

	i.mu.Lock()
	for {
		if time.Since(st) > time.Hour {
			i.mu.Unlock()
			return nil, fmt.Errorf("ref %v locked: %w", wOpts.Desc.Digest, errdefs.ErrUnavailable)
		}
		if _, ok := i.active[wOpts.Desc.Digest]; ok {
			i.c.Wait()
		} else {
			break
		}
	}

	i.active[wOpts.Desc.Digest] = struct{}{}
	i.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			i.mu.Lock()
			delete(i.active, wOpts.Desc.Digest)
			i.c.Broadcast()
			i.mu.Unlock()
		})
	}

	// pusher requires desc.MediaType to determine the PUT URL, especially for manifest blobs.
	contentWriter, err := i.p.Push(ctx, wOpts.Desc)
	if err != nil {
		release()
		return nil, err
	}
	runtime.SetFinalizer(contentWriter, func(_ content.Writer) {
		release()
	})
	return &pushWriter{
		Writer:           contentWriter,
		contentWriterRef: wOpts.Ref,
		release:          release,
	}, nil
}

type pushWriter struct {
	content.Writer          // returned from pusher.Push
	contentWriterRef string // ref passed for Writer()
	release          func()
}

func (w *pushWriter) Status() (content.Status, error) {
	st, err := w.Writer.Status()
	if err != nil {
		return st, err
	}
	if w.contentWriterRef != "" {
		st.Ref = w.contentWriterRef
	}
	return st, nil
}

func (w *pushWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	err := w.Writer.Commit(ctx, size, expected, opts...)
	if w.release != nil {
		w.release()
	}
	return err
}

func (w *pushWriter) Close() error {
	err := w.Writer.Close()
	if w.release != nil {
		w.release()
	}
	return err
}
