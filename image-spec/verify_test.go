// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/plugins/content/local"
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

func describe(data []byte) ocispec.Descriptor {
	return ocispec.Descriptor{Digest: digest.FromBytes(data), Size: int64(len(data))}
}

// tampered returns a copy of data with its last byte changed.
func tampered(data []byte) []byte {
	other := bytes.Clone(data)
	other[len(other)-1] ^= 0xff
	return other
}

func TestReadVerifiedBlob(t *testing.T) {
	t.Parallel()

	data := []byte("a manifest")

	t.Run("a blob that matches its digest", func(t *testing.T) {
		t.Parallel()

		p := contentutil.FromFetcher(served{describe(data).Digest: data})
		got, err := readVerifiedBlob(t.Context(), p, describe(data))
		require.NoError(t, err)
		assert.Equal(t, data, got)
	})

	t.Run("a blob that does not match its digest is refused", func(t *testing.T) {
		t.Parallel()

		p := contentutil.FromFetcher(served{describe(data).Digest: tampered(data)})
		_, err := readVerifiedBlob(t.Context(), p, describe(data))
		require.ErrorIs(t, err, ErrDigestMismatch)
	})

	t.Run("a blob with a digest that does not parse is refused", func(t *testing.T) {
		t.Parallel()

		desc := ocispec.Descriptor{Digest: "sha256:nope", Size: 1}
		p := contentutil.FromFetcher(served{desc.Digest: []byte("x")})
		_, err := readVerifiedBlob(t.Context(), p, desc)
		require.ErrorIs(t, err, digest.ErrDigestInvalidLength)
	})
}

// layer returns a tar layer of a kernel, followed by another file.
func layer(t *testing.T, kernel []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range []struct {
		name string
		data []byte
	}{{"kernel", kernel}, {"other", []byte("other")}} {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.data))}))
		_, err := tw.Write(f.data)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())

	return buf.Bytes()
}

func TestContentStoreFileVerified(t *testing.T) {
	t.Parallel()

	kernel := []byte("kernel")
	blob := layer(t, kernel)
	desc := describe(blob)

	open := func(t *testing.T, path string, serves []byte) io.ReadCloser {
		t.Helper()

		p := contentutil.FromFetcher(served{desc.Digest: serves})
		rc, _, err := NewContentStoreFile(p, desc, path).Open(t.Context())
		require.NoError(t, err)

		return rc
	}

	for _, tc := range []struct {
		name string
		path string
		want []byte
	}{
		{"a layer", "", blob},
		{"a file of a layer", "/kernel", kernel},
	} {
		t.Run(tc.name+" that matches its digest is read whole", func(t *testing.T) {
			t.Parallel()

			rc := open(t, tc.path, blob)
			got, err := io.ReadAll(rc)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			require.NoError(t, rc.Close())
		})

		t.Run(tc.name+" that does not match its digest fails before its last bytes", func(t *testing.T) {
			t.Parallel()

			rc := open(t, tc.path, tampered(blob))
			_, err := io.ReadFull(rc, make([]byte, len(tc.want)))
			require.ErrorIs(t, err, ErrDigestMismatch)
			require.ErrorIs(t, rc.Close(), ErrDigestMismatch)
		})
	}

	t.Run("a blob with a negative size is refused", func(t *testing.T) {
		t.Parallel()

		desc := ocispec.Descriptor{Digest: digest.FromBytes(blob), Size: -1}
		p := contentutil.FromFetcher(served{desc.Digest: blob})
		_, _, err := NewContentStoreFile(p, desc, "").Open(t.Context())
		require.Error(t, err)
	})

	t.Run("a file of a layer that stops early fails to close, leaving the rest of the layer unread", func(t *testing.T) {
		t.Parallel()

		r := bytes.NewReader(blob)
		p := contentutil.FromFetcher(fetcherFunc(func() io.Reader { return r }))
		rc, _, err := NewContentStoreFile(p, desc, "/kernel").Open(t.Context())
		require.NoError(t, err)
		_, err = rc.Read(make([]byte, 1))
		require.NoError(t, err)
		require.ErrorContains(t, rc.Close(), "unverified")
		assert.NotZero(t, r.Len())
	})
}

// fetcherFunc is a registry that serves what it returns for any digest.
type fetcherFunc func() io.Reader

func (f fetcherFunc) Fetch(context.Context, ocispec.Descriptor) (io.ReadCloser, error) {
	return io.NopCloser(f()), nil
}

// unchecked is an ingester that commits what it is given unchecked, as a
// pusher does.
type unchecked struct {
	content.Ingester
}

func (i unchecked) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	w, err := i.Ingester.Writer(ctx, opts...)
	if err != nil {
		return nil, err
	}

	return uncheckedWriter{w}, nil
}

type uncheckedWriter struct {
	content.Writer
}

func (w uncheckedWriter) Commit(ctx context.Context, _ int64, _ digest.Digest, opts ...content.Opt) error {
	return w.Writer.Commit(ctx, 0, "", opts...)
}

func TestSaveContentVerifiesWhatItCopies(t *testing.T) {
	t.Parallel()

	data := []byte("rom data")
	desc := ocispec.Descriptor{MediaType: MediaTypeRom, Digest: digest.FromBytes(data), Size: int64(len(data))}
	p := contentutil.FromFetcher(served{desc.Digest: tampered(data)})

	img := *testImage
	img.Roms = []File{NewContentStoreFile(p, desc, "")}
	dst, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	_, err = SaveContent(t.Context(), unchecked{dst}, "latest", &img)
	require.ErrorIs(t, err, ErrDigestMismatch)
}
