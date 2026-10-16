// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/containerd/containerd/v2/core/content"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ErrDigestMismatch is the error for content that is not what its descriptor
// names.
var ErrDigestMismatch = errors.New("content does not match its digest")

// maxMemoryBlob is the largest blob that verifiedProvider holds in memory. A
// larger one waits in a temporary file without a name.
const maxMemoryBlob = 1 << 20

// verifiedProvider reads each blob of its provider whole, and checks it
// against the size and digest of its descriptor before anything reads it: a
// registry cannot serve other content for a digest, even to a reader that
// stops early, such as one that takes a file out of a layer.
type verifiedProvider struct {
	content.Provider
}

func (p verifiedProvider) ReaderAt(ctx context.Context, desc ocispec.Descriptor) (content.ReaderAt, error) {
	if err := desc.Digest.Validate(); err != nil {
		return nil, fmt.Errorf("blob %q: %w", desc.Digest, err)
	}

	ra, err := p.Provider.ReaderAt(ctx, desc)
	if err != nil {
		return nil, err
	}
	defer ra.Close()
	src := io.NewSectionReader(ra, 0, desc.Size)

	if desc.Size <= maxMemoryBlob {
		var buf bytes.Buffer
		if err := copyVerified(&buf, src, desc); err != nil {
			return nil, err
		}
		return memoryBlob{bytes.NewReader(buf.Bytes())}, nil
	}

	f, err := os.CreateTemp("", "blob-")
	if err != nil {
		return nil, fmt.Errorf("buffering blob %s: %w", desc.Digest, err)
	}
	// Without its name, the file is gone once it is closed, also by a reader
	// that never closes it, or a process that dies. Windows cannot remove an
	// open file, so there Close removes it.
	blob := fileBlob{File: f, size: desc.Size, unlinked: os.Remove(f.Name()) == nil}
	if err := copyVerified(f, src, desc); err != nil {
		return nil, errors.Join(err, blob.Close())
	}

	return blob, nil
}

// copyVerified copies the blob that desc names from r to w, and fails unless
// it is exactly the size and digest of desc.
func copyVerified(w io.Writer, r io.Reader, desc ocispec.Descriptor) error {
	verifier := desc.Digest.Verifier()
	n, err := io.Copy(io.MultiWriter(w, verifier), r)
	if err != nil {
		return fmt.Errorf("reading blob %s: %w", desc.Digest, err)
	}
	if n != desc.Size || !verifier.Verified() {
		return fmt.Errorf("%w: blob %s", ErrDigestMismatch, desc.Digest)
	}

	return nil
}

// memoryBlob is a checked blob in memory.
type memoryBlob struct {
	*bytes.Reader
}

func (memoryBlob) Close() error { return nil }

// fileBlob is a checked blob in a temporary file.
type fileBlob struct {
	*os.File
	size     int64
	unlinked bool // else Close removes it
}

func (b fileBlob) Size() int64 { return b.size }

func (b fileBlob) Close() error {
	err := b.File.Close()
	if !b.unlinked {
		err = errors.Join(err, os.Remove(b.Name()))
	}

	return err
}
