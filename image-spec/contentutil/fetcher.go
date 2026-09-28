// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026, The BuildKit Authors.
// Licensed under the Apache License, Version 2.0 (the "License").
// You may not use this file except in compliance with the License.

package contentutil

import (
	"context"
	"errors"
	"io"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/remotes"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// FromFetcher mirrors buildkit's contentutil.FromFetcher.
// Source: github.com/moby/buildkit/util/contentutil/fetcher.go
func FromFetcher(f remotes.Fetcher) content.Provider {
	return &fetchedProvider{f: f}
}

type fetchedProvider struct {
	f remotes.Fetcher
}

func (p *fetchedProvider) ReaderAt(ctx context.Context, desc ocispec.Descriptor) (content.ReaderAt, error) {
	rc, err := p.f.Fetch(ctx, desc)
	if err != nil {
		return nil, err
	}

	return &readerAt{Reader: rc, Closer: rc, size: desc.Size}, nil
}

type readerAt struct {
	io.Reader
	io.Closer
	size   int64
	offset int64
}

func (r *readerAt) ReadAt(b []byte, off int64) (int, error) {
	if r.offset != off {
		if seeker, ok := r.Reader.(io.Seeker); ok {
			if _, err := seeker.Seek(off, io.SeekStart); err != nil {
				return 0, err
			}
			r.offset = off
		} else {
			if ra, ok := r.Reader.(io.ReaderAt); ok {
				return ra.ReadAt(b, off)
			}
			return 0, errors.New("unsupported offset")
		}
	}

	var totalN int
	for len(b) > 0 {
		n, err := r.Read(b)
		if errors.Is(err, io.EOF) && n == len(b) {
			err = nil
		}
		r.offset += int64(n)
		totalN += n
		b = b[n:]
		if err != nil {
			return totalN, err
		}
	}
	return totalN, nil
}

func (r *readerAt) Size() int64 {
	return r.size
}
