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

// Compression is how Save compresses the tar layers that it writes for the kernel, the kernel debug image and the initrd.
type Compression string

const (
	// CompressionNone leaves the layers uncompressed.
	CompressionNone Compression = ""
	// CompressionZstd compresses the tar layers of the kernel, the kernel debug image and the initrd with zstd.
	CompressionZstd Compression = "zstd"
)

// maxZstdWindow bounds what a frame can make the decoder allocate: 8 MiB, the window of the encoder and the limit of the agent.
const maxZstdWindow = 8 << 20

// maxLayerSize bounds the decompressed bytes that a load or a save reads from the layer of a part:
// 64 GiB, the default limit of the agent for a kernel, an initrd or a ROM, plus 64 MiB for the tar headers and padding.
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

// decompressedDigest returns the digest of the content of the compressed layer r of mediaType, and reads r to its end.
func decompressedDigest(ctx context.Context, mediaType string, r io.Reader, limit int64) (digest.Digest, error) {
	d, err := decompressLayer(mediaType, r)
	if err != nil {
		return "", err
	}
	digester := digest.Canonical.Digester()
	_, err = io.Copy(digester.Hash(), &boundedReader{ctx: ctx, r: d, limit: limit})
	if err = errors.Join(err, d.Close()); err != nil {
		return "", err
	}
	if _, err := io.Copy(io.Discard, r); err != nil {
		return "", err
	}
	return digester.Digest(), nil
}

// layerDiffID returns the digest of the content of the compressed layer desc, which it reads from input.
func layerDiffID(ctx context.Context, input content.Provider, desc ocispec.Descriptor, limit int64) (digest.Digest, error) {
	vr, err := openVerified(ctx, input, desc)
	if err != nil {
		return "", err
	}
	defer vr.Close()
	id, err := decompressedDigest(ctx, desc.MediaType, vr, limit)
	if err != nil {
		return "", fmt.Errorf("failed to decompress layer %s: %w", desc.Digest, err)
	}
	return id, nil
}

// diffIDWriter computes the DiffID of a compressed layer from the bytes of its blob that are written to it.
type diffIDWriter struct {
	desc   ocispec.Descriptor
	pw     *io.PipeWriter
	done   chan struct{}
	id     digest.Digest
	err    error
	failed bool // a write failed, as the decompression stopped with an error
}

func newDiffIDWriter(ctx context.Context, desc ocispec.Descriptor, limit int64) *diffIDWriter {
	pr, pw := io.Pipe()
	w := &diffIDWriter{desc: desc, pw: pw, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		w.id, w.err = decompressedDigest(ctx, desc.MediaType, pr, limit)
		// A write after an error fails with that error.
		pr.CloseWithError(w.err)
	}()
	return w
}

func (w *diffIDWriter) Write(p []byte) (int, error) {
	n, err := w.pw.Write(p)
	if err != nil {
		w.failed = true
	}
	return n, err
}

// finish ends the blob after a write of it that stopped with err, nil at its end, and returns the DiffID.
// It returns an error only if the decompression failed: the caller handles err.
func (w *diffIDWriter) finish(err error) (digest.Digest, error) {
	w.pw.CloseWithError(err)
	<-w.done
	switch {
	case w.failed, err == nil && w.err != nil:
		return "", fmt.Errorf("failed to decompress layer %s: %w", w.desc.Digest, w.err)
	case err != nil:
		return "", nil
	}
	return w.id, nil
}
