// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package rootfs

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"unikraft.com/x/mergefs"
)

const (
	// maxZstdWindow bounds the window of a zstd layer, as the agent does, so that a small layer cannot make the reader allocate much memory.
	maxZstdWindow = 128 << 20

	gzipMagic = "\x1f\x8b"
	zstdMagic = "\x28\xb5\x2f\xfd"
	// zstdSkippableMagic is the magic of a zstd skippable frame, with any value in the low 4 bits.
	zstdSkippableMagic uint32 = 0x184d2a50
)

// IsLayer reports if NewLayer accepts the media type: a plain, gzip or zstd tar layer of OCI or Docker.
func IsLayer(mediaType string) bool {
	switch mediaType {
	case ocispec.MediaTypeImageLayer, ocispec.MediaTypeImageLayerGzip, ocispec.MediaTypeImageLayerZstd,
		ocispec.MediaTypeImageLayerNonDistributable, ocispec.MediaTypeImageLayerNonDistributableGzip, //nolint:staticcheck // Old images still use them.
		ocispec.MediaTypeImageLayerNonDistributableZstd, //nolint:staticcheck // Old images still use it.
		images.MediaTypeDockerSchema2Layer, images.MediaTypeDockerSchema2LayerGzip, images.MediaTypeDockerSchema2LayerZstd:
		return true
	}
	return false
}

// NewLayer returns the [mergefs.Layer] of a blob that [IsLayer] accepts, with the compression from its first bytes.
func NewLayer(mediaType string, open func() (io.ReadCloser, error)) (mergefs.Layer, error) {
	if !IsLayer(mediaType) {
		return nil, fmt.Errorf("unsupported layer media type %q", mediaType)
	}
	return func() (io.ReadCloser, error) {
		blob, err := open()
		if err != nil {
			return nil, err
		}
		rc, err := decompress(blob)
		if err != nil {
			_ = blob.Close()
			return nil, err
		}
		return rc, nil
	}, nil
}

func decompress(blob io.ReadCloser) (io.ReadCloser, error) {
	br := bufio.NewReader(blob)
	magic, err := br.Peek(len(zstdMagic))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	d := &decompressed{Reader: br, blob: blob, br: br}
	switch {
	case bytes.HasPrefix(magic, []byte(gzipMagic)):
		g, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("gzip layer: %w", err)
		}
		d.Reader = g
		d.close = g.Close
	case bytes.HasPrefix(magic, []byte(zstdMagic)),
		len(magic) == len(zstdMagic) && binary.LittleEndian.Uint32(magic)&^0xf == zstdSkippableMagic:
		z, err := zstd.NewReader(br, zstd.WithDecoderMaxWindow(maxZstdWindow))
		if err != nil {
			return nil, fmt.Errorf("zstd layer: %w", err)
		}
		d.Reader = z
		d.close = func() error { z.Close(); return nil }
	}
	return d, nil
}

// decompressed is the tar stream of a blob.
type decompressed struct {
	io.Reader
	blob  io.ReadCloser
	br    *bufio.Reader
	close func() error
	eof   bool
}

func (d *decompressed) Read(p []byte) (int, error) {
	n, err := d.Reader.Read(p)
	if err == io.EOF {
		d.eof = true
	}
	return n, err
}

// Close reads the rest of the blob after the end of the tar stream, for a verifying reader.
func (d *decompressed) Close() error {
	if !d.eof {
		// The zstd decoder waits for its read of the blob, which closing the blob ends.
		err := d.blob.Close()
		if d.close != nil {
			err = errors.Join(err, d.close())
		}
		return err
	}
	_, err := io.Copy(io.Discard, d.br)
	if d.close != nil {
		err = errors.Join(err, d.close())
	}
	return errors.Join(err, d.blob.Close())
}
