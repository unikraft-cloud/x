// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package rootfs

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"unikraft.com/x/mergefs"
)

// blob is a layer blob that records if it was read to the end and closed.
type blob struct {
	*bytes.Reader
	closed bool
}

func (b *blob) Close() error { b.closed = true; return nil }

func compress(t *testing.T, kind string, data []byte) []byte {
	var buf bytes.Buffer
	var w io.WriteCloser
	switch kind {
	case "gzip":
		w = gzip.NewWriter(&buf)
	case "zstd":
		z, err := zstd.NewWriter(&buf)
		require.NoError(t, err)
		w = z
	default:
		return data
	}
	_, err := w.Write(data)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func TestNewLayer(t *testing.T) {
	t.Parallel()

	tarData := tarOf(t, "a", "1", "d/", "", "d/b", "2")
	for _, mediaType := range []string{
		ocispec.MediaTypeImageLayer, ocispec.MediaTypeImageLayerGzip, ocispec.MediaTypeImageLayerZstd,
		images.MediaTypeDockerSchema2Layer, images.MediaTypeDockerSchema2LayerGzip, images.MediaTypeDockerSchema2LayerZstd,
	} {
		// The compression comes from the data, also when the media type is wrong.
		for _, kind := range []string{"plain", "gzip", "zstd"} {
			t.Run(mediaType+" with "+kind+" data", func(t *testing.T) {
				b := &blob{Reader: bytes.NewReader(compress(t, kind, tarData))}
				layer, err := NewLayer(mediaType, func() (io.ReadCloser, error) { return b, nil })
				require.NoError(t, err)

				f, err := mergefs.Merge(context.Background(), []mergefs.Layer{layer}, mergefs.InMemory())
				require.NoError(t, err)
				got, err := fs.ReadFile(f, "d/b")
				require.NoError(t, err)
				require.Equal(t, "2", string(got))
				require.Zero(t, b.Len(), "the blob must be read to the end")
				require.True(t, b.closed)
			})
		}
	}
}

func TestNewLayerUnsupported(t *testing.T) {
	t.Parallel()

	for _, mediaType := range []string{
		ocispec.MediaTypeImageLayer + "+encrypted",
		images.MediaTypeDockerSchema2LayerForeignGzip,
		images.MediaTypeErofsLayer,
		ocispec.MediaTypeImageConfig,
	} {
		t.Run(mediaType, func(t *testing.T) {
			_, err := NewLayer(mediaType, nil)
			require.ErrorContains(t, err, "unsupported layer media type")
		})
	}
}

func TestNewLayerCorrupt(t *testing.T) {
	t.Parallel()

	data := compress(t, "gzip", tarOf(t, "a", "1"))
	b := &blob{Reader: bytes.NewReader(data[:len(data)/2])}
	layer, err := NewLayer(ocispec.MediaTypeImageLayerGzip, func() (io.ReadCloser, error) { return b, nil })
	require.NoError(t, err)
	_, err = mergefs.Merge(context.Background(), []mergefs.Layer{layer}, mergefs.InMemory())
	require.Error(t, err)
	require.True(t, b.closed)
}

// stalling is a blob that blocks after its data until it is closed, as a stalled download.
type stalling struct {
	*bytes.Reader
	done chan struct{}
	once sync.Once
}

func (s *stalling) Read(p []byte) (int, error) {
	if n, err := s.Reader.Read(p); err != io.EOF {
		return n, err
	}
	<-s.done
	return 0, io.ErrClosedPipe
}

func (s *stalling) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

func TestNewLayerStalledBlob(t *testing.T) {
	t.Parallel()

	// A block that is not a tar header, then a download that stalls.
	b := &stalling{Reader: bytes.NewReader(compress(t, "zstd", bytes.Repeat([]byte{0xff}, 512))), done: make(chan struct{})}
	layer, err := NewLayer(ocispec.MediaTypeImageLayerZstd, func() (io.ReadCloser, error) { return b, nil })
	require.NoError(t, err)
	errc := make(chan error, 1)
	go func() {
		_, err := mergefs.Merge(context.Background(), []mergefs.Layer{layer}, mergefs.InMemory())
		errc <- err
	}()
	select {
	case err := <-errc:
		require.Error(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the merge waits for the stalled blob")
	}
}

func TestNewLayerZstdSkippableFrame(t *testing.T) {
	t.Parallel()

	// A zstd stream can start with a skippable frame: a magic, a little-endian size and that many bytes.
	frame := []byte{0x5a, 0x2a, 0x4d, 0x18, 3, 0, 0, 0, 'a', 'b', 'c'}
	data := append(frame, compress(t, "zstd", tarOf(t, "a", "1"))...)
	layer, err := NewLayer(ocispec.MediaTypeImageLayerZstd, func() (io.ReadCloser, error) {
		return &blob{Reader: bytes.NewReader(data)}, nil
	})
	require.NoError(t, err)
	f, err := mergefs.Merge(context.Background(), []mergefs.Layer{layer}, mergefs.InMemory())
	require.NoError(t, err)
	got, err := fs.ReadFile(f, "a")
	require.NoError(t, err)
	require.Equal(t, "1", string(got))
}

// tarOf returns a tar stream of name and data pairs. A name that ends in a slash is a directory.
func tarOf(t *testing.T, pairs ...string) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i := 0; i < len(pairs); i += 2 {
		hdr := &tar.Header{Typeflag: tar.TypeReg, Name: pairs[i], Mode: 0o644, Size: int64(len(pairs[i+1]))}
		if strings.HasSuffix(pairs[i], "/") {
			hdr.Typeflag, hdr.Mode = tar.TypeDir, 0o755
		}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err := tw.Write([]byte(pairs[i+1]))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

// zstdFrame returns an empty zstd frame whose header asks for a window of 1<<windowLog bytes.
func zstdFrame(windowLog int) []byte {
	return []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, byte(windowLog-10) << 3, 0x01, 0x00, 0x00}
}

func TestNewLayerZstdWindow(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		windowLog int
		ok        bool
	}{{23, true}, {27, true}, {28, false}} {
		layer, err := NewLayer(ocispec.MediaTypeImageLayerZstd, func() (io.ReadCloser, error) {
			return &blob{Reader: bytes.NewReader(zstdFrame(tc.windowLog))}, nil
		})
		require.NoError(t, err)
		rc, err := layer()
		require.NoError(t, err)
		_, err = io.ReadAll(rc)
		if tc.ok {
			require.NoError(t, err, tc.windowLog)
		} else {
			require.ErrorIs(t, err, zstd.ErrWindowSizeExceeded, tc.windowLog)
		}
		require.NoError(t, rc.Close())
	}
}
