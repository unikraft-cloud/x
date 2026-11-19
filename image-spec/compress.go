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
	"github.com/containerd/containerd/v2/core/images"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Compression is how a save compresses the layers that it packages for the parts of an image; it copies other parts as they are.
type Compression string

const (
	// CompressionNone leaves the layers uncompressed.
	CompressionNone Compression = ""
	// CompressionZstd compresses the tar layers of the kernel, the kernel debug image and the initrd with zstd.
	CompressionZstd Compression = "zstd"
)

// maxZstdWindow is the largest window of the zstd levels, which bounds what a frame can make the decoder allocate.
const maxZstdWindow = 128 << 20

// maxLayerSize bounds the decompressed bytes that a load or a save reads from
// the layer of a part: the default limit of the agent for a kernel, an initrd
// or a ROM, 64 GiB, plus 64 MiB for the tar headers and padding.
const maxLayerSize int64 = 64<<30 + 64<<20

// ErrLayerTooLarge is the error of a read of a layer that decompresses to more than the limit.
var ErrLayerTooLarge = errors.New("decompressed layer is too large")

// boundedReader reads at most limit bytes from r, and stops when ctx ends.
type boundedReader struct {
	ctx   context.Context
	r     io.Reader
	limit int64
	read  int64
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if b.read > b.limit {
		return 0, fmt.Errorf("%w: more than %d bytes", ErrLayerTooLarge, b.limit)
	}
	if left := b.limit - b.read; int64(len(p)) > left+1 {
		p = p[:left+1]
	}
	n, err := b.r.Read(p)
	b.read += int64(n)
	if b.read > b.limit {
		return n - int(b.read-b.limit), fmt.Errorf("%w: more than %d bytes", ErrLayerTooLarge, b.limit)
	}
	return n, err
}

// layerDecompressors decompress the compressed tar layers, by media type.
var layerDecompressors = map[string]func(io.Reader) (io.ReadCloser, error){
	ocispec.MediaTypeImageLayerGzip:        newGzipReader,
	images.MediaTypeDockerSchema2LayerGzip: newGzipReader,
	ocispec.MediaTypeImageLayerZstd: func(r io.Reader) (io.ReadCloser, error) {
		d, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(maxZstdWindow))
		if err != nil {
			return nil, err
		}
		return d.IOReadCloser(), nil
	},
}

func newGzipReader(r io.Reader) (io.ReadCloser, error) {
	return gzip.NewReader(r)
}

// isCompressedLayer reports whether mediaType is a compressed tar layer.
func isCompressedLayer(mediaType string) bool {
	_, ok := layerDecompressors[mediaType]
	return ok
}

// decompressLayer returns the tar stream of the layer blob r, of mediaType.
func decompressLayer(mediaType string, r io.Reader) (io.ReadCloser, error) {
	if decompress, ok := layerDecompressors[mediaType]; ok {
		return decompress(r)
	}
	return io.NopCloser(r), nil
}

// newZstdWriter returns a writer that compresses to w with zstd.
func newZstdWriter(w io.Writer) (io.WriteCloser, error) {
	// NOTE: one encoder, so that the same input gives the same blob with one version of the library.
	return zstd.NewWriter(w, zstd.WithEncoderConcurrency(1))
}

// diffIDFile is a File that knows the DiffID of the layer that holds it.
type diffIDFile interface {
	layerDiffID() digest.Digest
}

// layerDiffID returns the digest of the uncompressed content of the layer desc that file copies from provider.
func layerDiffID(ctx context.Context, file File, provider content.Provider, desc ocispec.Descriptor) (digest.Digest, error) {
	return layerDiffIDWithin(ctx, file, provider, desc, maxLayerSize)
}

// layerDiffIDWithin is layerDiffID with a limit on the decompressed bytes.
func layerDiffIDWithin(ctx context.Context, file File, provider content.Provider, desc ocispec.Descriptor, limit int64) (digest.Digest, error) {
	if !isCompressedLayer(desc.MediaType) {
		return desc.Digest, nil
	}
	if f, ok := file.(diffIDFile); ok && f.layerDiffID() != "" {
		return f.layerDiffID(), nil
	}
	ra, err := provider.ReaderAt(ctx, desc)
	if err != nil {
		return "", fmt.Errorf("failed to read layer %s: %w", desc.Digest, err)
	}
	vr, err := newVerifiedReader(ra, desc)
	if err != nil {
		ra.Close()
		return "", err
	}
	defer vr.Close()
	r, err := decompressLayer(desc.MediaType, vr)
	if err != nil {
		return "", fmt.Errorf("failed to decompress layer %s: %w", desc.Digest, err)
	}
	defer r.Close()
	digester := digest.Canonical.Digester()
	if _, err := io.Copy(digester.Hash(), &boundedReader{ctx: ctx, r: r, limit: limit}); err != nil {
		return "", fmt.Errorf("failed to decompress layer %s: %w", desc.Digest, err)
	}
	if _, err := io.Copy(io.Discard, vr); err != nil {
		return "", fmt.Errorf("failed to read layer %s: %w", desc.Digest, err)
	}
	return digester.Digest(), nil
}
