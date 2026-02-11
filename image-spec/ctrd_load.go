// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func LoadContent(ctx context.Context, store content.Provider, desc ocispec.Descriptor) (*Image, error) {
	img := &Image{
		Provider:   store,
		Descriptor: desc,
	}

	// TODO: store the descriptor of the manifest in img.desc
	mfst, err := images.Manifest(ctx, store, desc, platforms.All)
	if err != nil {
		return nil, fmt.Errorf("failed to get image manifest: %w", err)
	}
	img.Annotations = mfst.Annotations

	dt, err := content.ReadBlob(ctx, store, mfst.Config)
	if err != nil {
		return nil, fmt.Errorf("failed to read image config blob: %w", err)
	}
	var config ocispec.Image
	if err := json.Unmarshal(dt, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal image config blob: %w", err)
	}
	img.Image = &config

	for _, layer := range mfst.Layers {
		switch layer.MediaType {
		// Classic OCI layer (old-style image layer).
		case ocispec.MediaTypeImageLayer, images.MediaTypeDockerSchema2Layer:
			if p := layer.Annotations[AnnotationKernelPath]; p != "" {
				img.Kernel = &fileAccessor{store: store, desc: layer, path: p}
			}
			if p := layer.Annotations[AnnotationKernelDbgPath]; p != "" {
				img.KernelDebug = &fileAccessor{store: store, desc: layer, path: p}
			}
			if p := layer.Annotations[AnnotationKernelInitrdPath]; p != "" {
				img.Initrd = &fileAccessor{store: store, desc: layer, path: p}
			}

		case MediaTypeRom:
			img.Roms = append(img.Roms, &fileAccessor{store: store, desc: layer})
		}
	}

	return img, nil
}

type fileAccessor struct {
	store content.Provider
	desc  ocispec.Descriptor
	path  string
}

func (f *fileAccessor) Path() string {
	return f.path
}

func (f *fileAccessor) Open(ctx context.Context) (rc io.ReadCloser, size int64, rerr error) {
	r, err := f.store.ReaderAt(ctx, f.desc)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		if rerr != nil {
			r.Close()
		}
	}()
	sr := io.NewSectionReader(r, 0, f.desc.Size)

	if f.path == "" {
		return readCloser{Reader: sr, Closer: r}, f.desc.Size, nil
	}

	target := filepath.Join("/", f.path)

	tr := tar.NewReader(sr)
	hdr, rr, err := readTarFile(tr, target)
	if err != nil {
		return nil, 0, err
	}
	return readCloser{Reader: rr, Closer: r}, hdr.Size, nil
}

func (f *fileAccessor) Cleanup() error {
	return nil
}

func (f *fileAccessor) Source() (ocispec.Descriptor, content.Provider) {
	return f.desc, f.store
}

func readTarFile(tr *tar.Reader, target string) (*tar.Header, io.Reader, error) {
	target = filepath.Join("/", target)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}

		if filepath.Join("/", hdr.Name) != target {
			continue
		}

		if t := hdr.Typeflag; t != tar.TypeReg {
			return nil, nil, fmt.Errorf("file has wrong type %d", t)
		}

		return hdr, tr, nil
	}

	return nil, nil, &fs.PathError{Op: "open", Path: target, Err: fs.ErrNotExist}
}

type readCloser struct {
	io.Reader
	io.Closer
}
