// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package rootfs

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"testing"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
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

	tarData := tarOf(t, []ent{reg("a", "1"), dir("d"), reg("d/b", "2")})
	for _, mediaType := range []string{
		ocispec.MediaTypeImageLayer, ocispec.MediaTypeImageLayerGzip, ocispec.MediaTypeImageLayerZstd,
		images.MediaTypeDockerSchema2Layer, images.MediaTypeDockerSchema2LayerGzip, images.MediaTypeDockerSchema2LayerZstd,
	} {
		// The compression comes from the data, also when the media type is wrong.
		for _, kind := range []string{"plain", "gzip", "zstd"} {
			b := &blob{Reader: bytes.NewReader(compress(t, kind, tarData))}
			layer, err := NewLayer(mediaType, func() (io.ReadCloser, error) { return b, nil })
			require.NoError(t, err, mediaType)

			f, err := Merge(context.Background(), []Layer{layer}, InMemory())
			require.NoError(t, err, "%s with %s data", mediaType, kind)
			got, err := fs.ReadFile(f, "d/b")
			require.NoError(t, err)
			require.Equal(t, "2", string(got))
			require.Zero(t, b.Len(), "%s with %s data: the blob must be read to the end", mediaType, kind)
			require.True(t, b.closed)
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
		_, err := NewLayer(mediaType, nil)
		require.ErrorContains(t, err, "unsupported layer media type", mediaType)
	}
}

func TestNewLayerCorrupt(t *testing.T) {
	t.Parallel()

	data := compress(t, "gzip", tarOf(t, []ent{reg("a", "1")}))
	b := &blob{Reader: bytes.NewReader(data[:len(data)/2])}
	layer, err := NewLayer(ocispec.MediaTypeImageLayerGzip, func() (io.ReadCloser, error) { return b, nil })
	require.NoError(t, err)
	_, err = Merge(context.Background(), []Layer{layer}, InMemory())
	require.Error(t, err)
	require.True(t, b.closed)
}
