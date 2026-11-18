// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
)

func LoadContent(ctx context.Context, store content.Provider, desc ocispec.Descriptor, platform platforms.MatchComparer) (*Image, error) {
	mfsts, err := ManifestDescriptors(ctx, store, desc, ignoringOSFeatures(platform))
	if err != nil {
		return nil, fmt.Errorf("failed to get image manifest: %w", err)
	}
	idx, err := readIndex(ctx, store, desc)
	if err != nil {
		return nil, err
	}
	img, err := loadCtrdImageMfst(ctx, store, desc, mfsts[0])
	if err != nil {
		return nil, err
	}
	if idx != nil {
		idx.loaded = mfsts[:1]
		img.IndexAnnotations = idx.annotations()
		img.index = idx
	}
	return img, nil
}

func LoadAllContent(ctx context.Context, store content.Provider, desc ocispec.Descriptor, platform platforms.MatchComparer) ([]*Image, error) {
	mfsts, err := ManifestDescriptors(ctx, store, desc, ignoringOSFeatures(platform))
	if err != nil {
		return nil, fmt.Errorf("failed to get image manifest: %w", err)
	}

	idx, err := readIndex(ctx, store, desc)
	if err != nil {
		return nil, err
	}
	if idx != nil {
		idx.loaded = mfsts
	}

	imgs := make([]*Image, len(mfsts))

	eg, ctx := errgroup.WithContext(ctx)
	for i, mfst := range mfsts {
		eg.Go(func() error {
			img, err := loadCtrdImageMfst(ctx, store, desc, mfst)
			if err != nil {
				return fmt.Errorf("failed to load image manifest: %w", err)
			}
			if idx != nil {
				img.IndexAnnotations = idx.annotations()
				img.index = idx
			}
			imgs[i] = img
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	return imgs, nil
}

// loadCtrdImageMfst loads the image whose manifest is desc, recording root as
// what it was loaded from.
func loadCtrdImageMfst(ctx context.Context, store content.Provider, root ocispec.Descriptor, desc ocispec.Descriptor) (*Image, error) {
	raw, err := readVerifiedBlob(ctx, store, desc)
	if err != nil {
		return nil, fmt.Errorf("failed to read image manifest: %w", err)
	}
	var mfst ocispec.Manifest
	if err := json.Unmarshal(raw, &mfst); err != nil {
		return nil, fmt.Errorf("failed to read image manifest: %w", err)
	}
	img := &Image{
		Descriptor:         root,
		ManifestDescriptor: desc,
		Annotations:        mfst.Annotations,
		provider:           store,
		manifest:           raw,
	}

	dt, err := readVerifiedBlob(ctx, store, mfst.Config)
	if err != nil {
		return nil, fmt.Errorf("failed to read image config blob: %w", err)
	}
	var config ocispec.Image
	if err := json.Unmarshal(dt, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal image config blob: %w", err)
	}
	img.Image = &config

	for i, layer := range mfst.Layers {
		part := func(p string) *ContentStoreFile {
			f := NewContentStoreFile(store, layer, p)
			if len(config.RootFS.DiffIDs) == len(mfst.Layers) {
				f.DiffID = config.RootFS.DiffIDs[i]
			}
			return f
		}
		switch layer.MediaType {
		// Classic OCI layer (old-style image layer), which can be compressed.
		case ocispec.MediaTypeImageLayer, images.MediaTypeDockerSchema2Layer, ocispec.MediaTypeImageLayerGzip,
			ocispec.MediaTypeImageLayerZstd, images.MediaTypeDockerSchema2LayerGzip:
			if p := layer.Annotations[AnnotationKernelPath]; p != "" {
				img.Kernel = part(p)
			}
			if p := layer.Annotations[AnnotationKernelDbgPath]; p != "" {
				img.KernelDebug = part(p)
			}
			if p := layer.Annotations[AnnotationKernelInitrdPath]; p != "" {
				img.Initrd = part(p)
			}

		case MediaTypeRom:
			img.Roms = append(img.Roms, NewContentStoreFile(store, layer, ""))
		}
	}

	return img, nil
}

// closerFunc is a Closer that calls itself.
type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// ContentStoreFile is a File backed by a content.Provider and OCI descriptor.
type ContentStoreFile struct {
	Store content.Provider
	Desc  ocispec.Descriptor
	Path_ string
	// DiffID is the digest of the uncompressed layer, if it is known.
	DiffID digest.Digest

	maxSize int64 // the limit of the decompressed bytes that Open reads, maxLayerSize if 0
}

// NewContentStoreFile creates a new ContentStoreFile.
func NewContentStoreFile(store content.Provider, desc ocispec.Descriptor, path string) *ContentStoreFile {
	return &ContentStoreFile{
		Store: store,
		Desc:  desc,
		Path_: path,
	}
}

func (f *ContentStoreFile) Path() string {
	return f.Path_
}

func (f *ContentStoreFile) Open(ctx context.Context) (rc io.ReadCloser, size int64, rerr error) {
	r, err := f.Store.ReaderAt(ctx, f.Desc)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		if rerr != nil {
			r.Close()
		}
	}()
	// NOTE: checked here: only an entry of the blob may be read and the rest
	// thrown away, so no writer ever sees the whole blob to check it.
	vr, err := newVerifiedReader(r, f.Desc)
	if err != nil {
		return nil, 0, err
	}

	if f.Path_ == "" {
		return vr, f.Desc.Size, nil
	}

	target := filepath.Join("/", f.Path_)

	blob, err := decompressLayer(f.Desc.MediaType, vr.blob)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to decompress layer %s: %w", f.Desc.Digest, err)
	}
	limit := f.maxSize
	if limit == 0 {
		limit = maxLayerSize
	}
	tr := tar.NewReader(&boundedReader{ctx: ctx, r: blob, limit: limit})
	hdr, rr, err := readTarFile(tr, target)
	if err != nil {
		blob.Close()
		return nil, 0, err
	}
	vr.entry, vr.remaining = rr, hdr.Size
	return struct {
		io.Reader
		io.Closer
	}{vr, closerFunc(func() error { return errors.Join(blob.Close(), vr.Close()) })}, hdr.Size, nil
}

func (f *ContentStoreFile) Cleanup() error {
	return nil
}

func (f *ContentStoreFile) Clone() (File, error) {
	c := NewContentStoreFile(f.Store, f.Desc, f.Path_)
	c.DiffID, c.maxSize = f.DiffID, f.maxSize
	return c, nil
}

func (f *ContentStoreFile) layerDiffID() digest.Digest {
	return f.DiffID
}

func (f *ContentStoreFile) Source() (ocispec.Descriptor, content.Provider) {
	return f.Desc, f.Store
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
