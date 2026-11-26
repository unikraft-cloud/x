// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/image-spec/contentutil"
)

// writeJSON writes v to store as a blob of mediaType.
func writeJSON(t *testing.T, store content.Ingester, mediaType string, v any) ocispec.Descriptor {
	t.Helper()

	data, err := json.Marshal(v)
	require.NoError(t, err)
	desc := ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(data), Size: int64(len(data))}
	require.NoError(t, content.WriteBlob(t.Context(), store, desc.Digest.String(), bytes.NewReader(data), desc))
	return desc
}

func mustStore(t *testing.T, dir string) content.Store {
	t.Helper()

	store, err := local.NewStore(dir)
	require.NoError(t, err)
	return store
}

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
		got, err := readVerifiedBlob(t.Context(), p, describe(data), maxConfigSize)
		require.NoError(t, err)
		assert.Equal(t, data, got)
	})

	t.Run("a blob that does not match its digest is refused", func(t *testing.T) {
		t.Parallel()

		p := contentutil.FromFetcher(served{describe(data).Digest: tampered(data)})
		_, err := readVerifiedBlob(t.Context(), p, describe(data), maxConfigSize)
		require.ErrorIs(t, err, ErrDigestMismatch)
	})

	t.Run("a blob with a digest that does not parse is refused", func(t *testing.T) {
		t.Parallel()

		desc := ocispec.Descriptor{Digest: "sha256:nope", Size: 1}
		p := contentutil.FromFetcher(served{desc.Digest: []byte("x")})
		_, err := readVerifiedBlob(t.Context(), p, desc, maxConfigSize)
		require.ErrorIs(t, err, digest.ErrDigestInvalidLength)
	})

	t.Run("a blob with a size out of range is refused before a read", func(t *testing.T) {
		t.Parallel()

		for _, size := range []int64{-1, maxConfigSize + 1} {
			desc := describe(data)
			desc.Size = size
			p := contentutil.FromFetcher(served{desc.Digest: data})
			_, err := readVerifiedBlob(t.Context(), p, desc, maxConfigSize)
			require.ErrorContains(t, err, fmt.Sprintf("size %d is not between", size))
		}
	})

	t.Run("a blob of another size than its descriptor is refused", func(t *testing.T) {
		t.Parallel()

		// A local store gives the size of the file, not that of the descriptor.
		big := bytes.Repeat([]byte("x"), 1<<20)
		store := mustStore(t, t.TempDir())
		require.NoError(t, content.WriteBlob(t.Context(), store, "big", bytes.NewReader(big), describe(big)))
		for _, size := range []int64{100, int64(len(big)) + 1} {
			desc := describe(big)
			desc.Size = size
			_, err := readVerifiedBlob(t.Context(), store, desc, maxConfigSize)
			require.ErrorContains(t, err, fmt.Sprintf("size is not the %d bytes of its descriptor", size))
		}
	})

	t.Run("a blob in its descriptor is not fetched", func(t *testing.T) {
		t.Parallel()

		desc := describe(data)
		desc.Data = data
		got, err := readVerifiedBlob(t.Context(), contentutil.FromFetcher(served{}), desc, maxConfigSize)
		require.NoError(t, err)
		assert.Equal(t, data, got)
	})
}

// largestRead is a provider that records the largest buffer that a read asks to fill.
type largestRead struct {
	content.Provider
	largest int
}

func (p *largestRead) ReaderAt(ctx context.Context, desc ocispec.Descriptor) (content.ReaderAt, error) {
	ra, err := p.Provider.ReaderAt(ctx, desc)
	if err != nil {
		return nil, err
	}
	return largestReadAt{ReaderAt: ra, largest: &p.largest}, nil
}

type largestReadAt struct {
	content.ReaderAt
	largest *int
}

func (r largestReadAt) ReadAt(b []byte, off int64) (int, error) {
	*r.largest = max(*r.largest, len(b))
	return r.ReaderAt.ReadAt(b, off)
}

func TestReadVerifiedBlobFalseSize(t *testing.T) {
	// A false size does not cause a large buffer.
	data := []byte("a manifest")
	desc := describe(data)
	desc.Size = maxConfigSize
	p := &largestRead{Provider: contentutil.FromFetcher(served{desc.Digest: data})}
	_, err := readVerifiedBlob(t.Context(), p, desc, maxConfigSize)
	require.ErrorContains(t, err, "size is not")
	require.Positive(t, p.largest)
	require.Less(t, p.largest, 1<<20)
}

func TestLoadConfigOverManifestLimit(t *testing.T) {
	ctx := t.Context()
	store := mustStore(t, t.TempDir())
	linux := ocispec.Platform{OS: "linux", Architecture: "amd64"}
	config := writeJSON(t, store, ocispec.MediaTypeImageConfig, ocispec.Image{
		Platform: linux,
		Config:   ocispec.ImageConfig{Env: []string{"BIG=" + strings.Repeat("x", int(docker.MaxManifestSize))}},
	})
	require.Greater(t, config.Size, docker.MaxManifestSize)
	mfst := writeJSON(t, store, ocispec.MediaTypeImageManifest, ocispec.Manifest{SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest, Config: config})
	// The entry has no platform, so the load reads the config to match it.
	idx := writeJSON(t, store, ocispec.MediaTypeImageIndex, ocispec.Index{SchemaVersion: 2, MediaType: ocispec.MediaTypeImageIndex, Manifests: []ocispec.Descriptor{mfst}})

	imgs, err := LoadAllContent(ctx, store, idx, platforms.OnlyStrict(linux))
	require.NoError(t, err)
	require.Len(t, imgs, 1)
	require.Equal(t, mfst.Digest, imgs[0].ManifestDescriptor.Digest)
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

		t.Run(tc.name+" served with more after it fails before its last bytes", func(t *testing.T) {
			t.Parallel()

			rc := open(t, tc.path, append(bytes.Clone(blob), "more"...))
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

// resetting is an ingester whose writers reset once at commit, as a pusher
// does when it re-sends an upload.
type resetting struct {
	content.Ingester
	resets atomic.Int32
}

func (i *resetting) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	w, err := i.Ingester.Writer(ctx, opts...)
	if err != nil {
		return nil, err
	}

	return &resettingWriter{Writer: w, resets: &i.resets}, nil
}

type resettingWriter struct {
	content.Writer
	reset  bool
	resets *atomic.Int32
}

func (w *resettingWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	if w.reset {
		return w.Writer.Commit(ctx, size, expected, opts...)
	}
	w.reset = true
	w.resets.Add(1)
	if err := w.Truncate(0); err != nil {
		return err
	}

	return content.ErrReset
}

func TestVerifiedReaderSeek(t *testing.T) {
	data := []byte("blob")
	vr, err := newVerifiedReader(inlineReaderAt{bytes.NewReader(data)}, describe(data))
	require.NoError(t, err)
	_, err = io.ReadFull(vr, make([]byte, 2))
	require.NoError(t, err)

	_, err = vr.Seek(1, io.SeekStart)
	require.Error(t, err)
	n, err := vr.Seek(0, io.SeekStart)
	require.NoError(t, err)
	require.Zero(t, n)
	got, err := io.ReadAll(vr)
	require.NoError(t, err)
	require.Equal(t, data, got)
	require.NoError(t, vr.Close())

	// A blob that does not match its digest fails however often it is read.
	bad, err := newVerifiedReader(inlineReaderAt{bytes.NewReader(tampered(data))}, describe(data))
	require.NoError(t, err)
	for range 2 {
		_, err = io.ReadAll(bad)
		require.ErrorIs(t, err, ErrDigestMismatch)
		_, err = bad.Seek(0, io.SeekStart)
		require.NoError(t, err)
	}
}

func TestSaveContentWritesAgainAfterReset(t *testing.T) {
	t.Parallel()

	dst, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	store := &resetting{Ingester: dst}

	// The store checks each digest on commit, so a layer written again wrong fails.
	_, err = SaveContent(t.Context(), store, "latest", testImage)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, store.resets.Load(), int32(3)) // the three layers at least
}
