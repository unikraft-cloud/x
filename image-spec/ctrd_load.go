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
	"strings"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
)

func LoadContent(ctx context.Context, store content.Provider, desc ocispec.Descriptor, platform platforms.MatchComparer) (*Image, error) {
	mfsts, err := manifest(ctx, store, desc, platform)
	if err != nil {
		return nil, fmt.Errorf("failed to get image manifest: %w", err)
	}
	return loadCtrdImageMfst(ctx, store, desc, mfsts[0])
}

func LoadAllContent(ctx context.Context, store content.Provider, desc ocispec.Descriptor, platform platforms.MatchComparer) ([]*Image, error) {
	mfsts, err := manifest(ctx, store, desc, platform)
	if err != nil {
		return nil, fmt.Errorf("failed to get image manifest: %w", err)
	}

	imgs := make([]*Image, len(mfsts))

	eg, ctx := errgroup.WithContext(ctx)
	for i, mfst := range mfsts {
		eg.Go(func() error {
			img, err := loadCtrdImageMfst(ctx, store, desc, mfst)
			if err != nil {
				return fmt.Errorf("failed to load image manifest: %w", err)
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

func loadCtrdImageMfst(ctx context.Context, store content.Provider, desc ocispec.Descriptor, mfst ocispec.Manifest) (*Image, error) {
	// TODO: store the descriptor of the manifest in img.desc
	img := &Image{
		Provider:    store,
		Descriptor:  desc,
		Annotations: mfst.Annotations,
	}

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
				img.Kernel = NewContentStoreFile(store, layer, p)
			}
			if p := layer.Annotations[AnnotationKernelDbgPath]; p != "" {
				img.KernelDebug = NewContentStoreFile(store, layer, p)
			}
			if p := layer.Annotations[AnnotationKernelInitrdPath]; p != "" {
				img.Initrd = NewContentStoreFile(store, layer, p)
			}

		case MediaTypeKernel:
			img.Kernel = NewContentStoreFile(store, layer, kernelPath(layer, WellKnownKernelPath))

		case MediaTypeKernelDebug:
			img.KernelDebug = NewContentStoreFile(store, layer, kernelPath(layer, WellKnownKernelDbgPath))

		case MediaTypeInitrd:
			img.Initrd = NewContentStoreFile(store, layer, kernelPath(layer, WellKnownInitrdPath))

		case MediaTypeRom:
			img.Roms = append(img.Roms, NewContentStoreFile(store, layer, ""))

		default:
			// A layer of a kind this reader does not know, in a namespace only
			// Unikraft writes, is refused rather than skipped.
			//
			// Skipping is what a reader must do with a layer of somebody else's
			// image -- a compressed container layer says nothing about a
			// unikernel and there are many of them. But skipping one of ours
			// leaves an image with no kernel and no error, and a caller then
			// boots nothing at all. That is the worst way for this to fail, and
			// it is what a reader older than the layer it is given would do.
			if unikraftMediaType(layer.MediaType) {
				return nil, fmt.Errorf(
					"layer %s is a %s, which this version does not understand: it was written by something newer",
					layer.Digest, layer.MediaType,
				)
			}
		}
	}

	return img, nil
}

// unikraftMediaType reports whether a media type is one of Unikraft's own.
//
// By prefix, so that a type added after this was built is still recognised as
// ours and refused loudly rather than passed over.
func unikraftMediaType(mediaType string) bool {
	return strings.HasPrefix(mediaType, MediaTypePrefix)
}

// kernelPath is where a layer says its content belongs, or the well known place
// for its kind.
//
// A layer carrying its kind in its media type has no need of an annotation to
// say what it is, but one may still say where it goes.
func kernelPath(layer ocispec.Descriptor, wellKnown string) string {
	for _, annotation := range []string{AnnotationKernelPath, AnnotationKernelDbgPath, AnnotationKernelInitrdPath} {
		if p := layer.Annotations[annotation]; p != "" {
			return p
		}
	}

	return wellKnown
}

// ContentStoreFile is a File backed by a content.Provider and OCI descriptor.
type ContentStoreFile struct {
	Store content.Provider
	Desc  ocispec.Descriptor
	Path_ string
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
	sr := io.NewSectionReader(r, 0, f.Desc.Size)

	if f.Path_ == "" {
		return readCloser{Reader: sr, Closer: r}, f.Desc.Size, nil
	}

	target := filepath.Join("/", f.Path_)

	tr := tar.NewReader(sr)
	hdr, rr, err := readTarFile(tr, target)
	if err != nil {
		return nil, 0, err
	}
	return readCloser{Reader: rr, Closer: r}, hdr.Size, nil
}

func (f *ContentStoreFile) Cleanup() error {
	return nil
}

func (f *ContentStoreFile) Clone() (File, error) {
	return NewContentStoreFile(f.Store, f.Desc, f.Path_), nil
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

type readCloser struct {
	io.Reader
	io.Closer
}
