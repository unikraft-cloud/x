// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package rootfs

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

var (
	gzipMagic = []byte{0x1f, 0x8b}
	zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}
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

// NewLayer returns the Layer of a blob that [IsLayer] accepts, with the compression from its first bytes.
func NewLayer(mediaType string, open func() (io.ReadCloser, error)) (Layer, error) {
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
	case bytes.HasPrefix(magic, gzipMagic):
		g, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("gzip layer: %w", err)
		}
		d.Reader, d.close = g, g.Close
	case bytes.HasPrefix(magic, zstdMagic):
		z, err := zstd.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("zstd layer: %w", err)
		}
		d.Reader, d.close = z, func() error { z.Close(); return nil }
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
	var err error
	if d.eof {
		_, err = io.Copy(io.Discard, d.br)
	}
	if d.close != nil {
		err = errors.Join(err, d.close())
	}
	return errors.Join(err, d.blob.Close())
}
