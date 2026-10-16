// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/image-spec/contentutil"
)

// served is a registry that serves data for each digest, whatever it is.
type served map[digest.Digest][]byte

func (s served) Fetch(_ context.Context, desc ocispec.Descriptor) (io.ReadCloser, error) {
	data, ok := s[desc.Digest]
	if !ok {
		return nil, errdefs.ErrNotFound
	}

	return io.NopCloser(bytes.NewReader(data)), nil
}

// fetch reads the blob that desc names through a verified provider of what
// the registry serves for it.
func fetch(t *testing.T, desc ocispec.Descriptor, serves []byte) (content.ReaderAt, error) {
	t.Helper()

	p := verifiedProvider{contentutil.FromFetcher(served{desc.Digest: serves})}

	return p.ReaderAt(t.Context(), desc)
}

// blobs are a blob held in memory and one that waits in a file.
var blobs = map[string][]byte{
	"in memory": []byte("a small blob, like a manifest"),
	"in a file": bytes.Repeat([]byte("layer"), maxMemoryBlob),
}

func describe(data []byte) ocispec.Descriptor {
	return ocispec.Descriptor{Digest: digest.FromBytes(data), Size: int64(len(data))}
}

// Not in parallel: the tests look at the temporary directory.
func TestVerifiedProvider(t *testing.T) {
	for name, data := range blobs {
		t.Run("a blob "+name+" that matches its digest is read whole", func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)

			ra, err := fetch(t, describe(data), data)
			require.NoError(t, err)
			assert.Equal(t, int64(len(data)), ra.Size())
			got, err := io.ReadAll(io.NewSectionReader(ra, 0, ra.Size()))
			require.NoError(t, err)
			assert.Equal(t, data, got)

			require.NoError(t, ra.Close())
			assertEmpty(t, tmp)
		})

		t.Run("a blob "+name+" that does not match its digest is refused", func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)

			other := bytes.Clone(data)
			other[0] ^= 0xff
			_, err := fetch(t, describe(data), other)
			require.ErrorIs(t, err, ErrDigestMismatch)
			assertEmpty(t, tmp)
		})
	}

	t.Run("a blob shorter than its descriptor is refused, even with its own digest", func(t *testing.T) {
		data := blobs["in memory"]
		desc := describe(data)
		desc.Size++
		_, err := fetch(t, desc, data)
		require.ErrorIs(t, err, ErrDigestMismatch)
	})

	t.Run("a blob with a digest that does not parse is refused", func(t *testing.T) {
		_, err := fetch(t, ocispec.Descriptor{Digest: "sha256:nope", Size: 1}, []byte("x"))
		require.ErrorIs(t, err, digest.ErrDigestInvalidLength)
	})
}

// assertEmpty asserts that nothing is left in dir.
func assertEmpty(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}
