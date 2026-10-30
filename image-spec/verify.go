// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ErrDigestMismatch is the error for content that is not what its descriptor
// names.
var ErrDigestMismatch = errors.New("content does not match its digest")

// readVerifiedBlob reads the blob that desc names, and fails unless it matches
// the digest of desc.
func readVerifiedBlob(ctx context.Context, provider content.Provider, desc ocispec.Descriptor) ([]byte, error) {
	if err := desc.Digest.Validate(); err != nil {
		return nil, fmt.Errorf("blob %q: %w", desc.Digest, err)
	}
	dt, err := content.ReadBlob(ctx, provider, desc)
	if err != nil {
		return nil, err
	}
	if desc.Digest.Algorithm().FromBytes(dt) != desc.Digest {
		return nil, fmt.Errorf("%w: blob %s", ErrDigestMismatch, desc.Digest)
	}

	return dt, nil
}

// verifiedReader reads an entry of a blob, by default the whole blob, hashing
// the blob as it goes. The read that would end the entry first reads the rest
// of the blob and checks its digest, and fails in place of the last bytes on
// a mismatch. Close fails too on a mismatch, or if the entry was not read to
// its end.
type verifiedReader struct {
	ra        content.ReaderAt
	blob      io.Reader
	entry     io.Reader
	remaining int64
	verifier  digest.Verifier
	digest    digest.Digest
	err       error // what every read returns once the entry is done
}

// newVerifiedReader returns a reader of the blob that desc names from ra,
// which it closes on Close.
func newVerifiedReader(ra content.ReaderAt, desc ocispec.Descriptor) (*verifiedReader, error) {
	if err := desc.Digest.Validate(); err != nil {
		return nil, fmt.Errorf("blob %q: %w", desc.Digest, err)
	}
	if desc.Size < 0 {
		return nil, fmt.Errorf("blob %s: invalid size %d", desc.Digest, desc.Size)
	}
	verifier := desc.Digest.Verifier()
	// A byte past desc, so a blob served with more after it fails its digest.
	tee := io.TeeReader(io.NewSectionReader(ra, 0, desc.Size+1), verifier)

	return &verifiedReader{
		ra:        ra,
		blob:      tee,
		entry:     tee,
		remaining: desc.Size,
		verifier:  verifier,
		digest:    desc.Digest,
	}, nil
}

func (r *verifiedReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}

	n, err := r.entry.Read(p[:min(int64(len(p)), r.remaining)])
	r.remaining -= int64(n)
	if r.remaining > 0 {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return n, err
	}

	if _, err := io.Copy(io.Discard, r.blob); err != nil {
		r.err = fmt.Errorf("reading blob %s: %w", r.digest, err)
		return 0, r.err
	}
	if !r.verifier.Verified() {
		r.err = fmt.Errorf("%w: blob %s", ErrDigestMismatch, r.digest)
		return 0, r.err
	}
	r.err = io.EOF

	return n, nil
}

func (r *verifiedReader) Close() error {
	err := r.err
	switch {
	case err == nil:
		err = fmt.Errorf("blob %s: closed with %d bytes unread, so unverified", r.digest, r.remaining)
	case errors.Is(err, io.EOF):
		err = nil
	}

	return errors.Join(err, r.ra.Close())
}
