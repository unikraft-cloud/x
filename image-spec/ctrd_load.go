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
	"maps"
	"path/filepath"
	"slices"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
)

// LoadOpt is an option of a load.
type LoadOpt func(*loadOptions)

type loadOptions struct {
	rootfsDir   string
	rootfsWrite RootfsWriter
}

func newLoadOptions(opts []LoadOpt) loadOptions {
	var o loadOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

func LoadContent(ctx context.Context, store content.Provider, desc ocispec.Descriptor, platform platforms.MatchComparer, opts ...LoadOpt) (*Image, error) {
	o := newLoadOptions(opts)
	mfsts, err := imageManifests(ctx, store, desc, platform)
	if isNoMatch(err) {
		imgs, err := loadFallback(ctx, store, desc, platform, err, o)
		if err != nil {
			return nil, err
		}
		for _, img := range imgs[1:] {
			_ = img.Close()
		}
		return imgs[0], nil
	}
	if err != nil {
		return nil, err
	}
	annotations, err := indexAnnotations(ctx, store, desc)
	if err != nil {
		return nil, err
	}
	img, err := loadCtrdImageMfst(ctx, store, desc, mfsts[0], platform, o)
	if err != nil {
		return nil, err
	}
	img.IndexAnnotations = annotations
	return img, nil
}

func LoadAllContent(ctx context.Context, store content.Provider, desc ocispec.Descriptor, platform platforms.MatchComparer, opts ...LoadOpt) ([]*Image, error) {
	o := newLoadOptions(opts)
	imgs, err := loadAll(ctx, store, desc, platform, platform, o)
	if isNoMatch(err) {
		return loadFallback(ctx, store, desc, platform, err, o)
	}
	return imgs, err
}

// loadAll loads the images of the manifests of desc that match selects, each for the platform.
func loadAll(ctx context.Context, store content.Provider, desc ocispec.Descriptor, match, platform platforms.MatchComparer, opts loadOptions) ([]*Image, error) {
	mfsts, err := imageManifests(ctx, store, desc, match)
	if err != nil {
		return nil, err
	}

	annotations, err := indexAnnotations(ctx, store, desc)
	if err != nil {
		return nil, err
	}

	imgs := make([]*Image, len(mfsts))

	eg, ctx := errgroup.WithContext(ctx)
	for i, mfst := range mfsts {
		eg.Go(func() error {
			img, err := loadCtrdImageMfst(ctx, store, desc, mfst, platform, opts)
			if err != nil {
				return fmt.Errorf("failed to load image manifest: %w", err)
			}
			img.IndexAnnotations = maps.Clone(annotations)
			imgs[i] = img
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		for _, img := range imgs {
			if img != nil {
				_ = img.Close()
			}
		}
		return nil, err
	}

	return imgs, nil
}

// annotationReferenceType marks an index entry that is no image, such as an attestation of BuildKit.
const annotationReferenceType = "vnd.docker.reference.type"

// imageManifests returns the manifests of the images of platform that desc names, without attestations.
func imageManifests(ctx context.Context, store content.Provider, desc ocispec.Descriptor, platform platforms.MatchComparer) ([]ocispec.Descriptor, error) {
	mfsts, err := ManifestDescriptors(ctx, store, desc, ignoringOSFeatures(platform))
	if err != nil {
		return nil, fmt.Errorf("failed to get image manifest: %w", err)
	}
	mfsts = slices.DeleteFunc(mfsts, func(mfst ocispec.Descriptor) bool {
		return mfst.Annotations[annotationReferenceType] == "attestation-manifest"
	})
	if len(mfsts) == 0 {
		return nil, fmt.Errorf("failed to get image manifest: %s has only attestations: %w", desc.Digest, errdefs.ErrNotFound)
	}
	return mfsts, nil
}

// loadCtrdImageMfst loads the image whose manifest is desc for the platform, and records root as its source.
func loadCtrdImageMfst(ctx context.Context, store content.Provider, root ocispec.Descriptor, desc ocispec.Descriptor, platform platforms.MatchComparer, opts loadOptions) (*Image, error) {
	mfst, err := readManifest(ctx, store, desc)
	if err != nil {
		return nil, fmt.Errorf("failed to read image manifest: %w", err)
	}
	img := &Image{
		Descriptor:         root,
		ManifestDescriptor: desc,
		Annotations:        mfst.Annotations,
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

	if IsGeneric(mfst, config.Platform) {
		img.Runtime = RuntimeKindDocker
		for _, layer := range mfst.Layers {
			img.Layers = append(img.Layers, NewContentStoreFile(store, layer, ""))
		}
		// A build that loads an OCI rootfs asks for linux, thus only a load for a Unikraft platform converts the image.
		// The match ignores OS features, as the selection of the manifest does.
		if platform != nil && !ignoringOSFeatures(platform).Match(config.Platform) && (genericPlatforms{platform}).Match(config.Platform) {
			img.Initrd = newRootfsFile(store, mfst.Layers, opts)
		}
		return img, nil
	}

	for _, layer := range mfst.Layers {
		part := true
		switch layer.MediaType {
		// Classic OCI layer (old-style image layer).
		case ocispec.MediaTypeImageLayer, images.MediaTypeDockerSchema2Layer:
			part = false
			if p := layer.Annotations[AnnotationKernelPath]; p != "" {
				img.Kernel = NewContentStoreFile(store, layer, p)
				part = true
			}
			if p := layer.Annotations[AnnotationKernelDbgPath]; p != "" {
				img.KernelDebug = NewContentStoreFile(store, layer, p)
				part = true
			}
			if p := layer.Annotations[AnnotationKernelInitrdPath]; p != "" {
				img.Initrd = NewContentStoreFile(store, layer, p)
				part = true
			}

		case MediaTypeRom:
			img.Roms = append(img.Roms, NewContentStoreFile(store, layer, ""))

		default:
			part = false
		}
		// A linux image that is no Docker image loads without its layers, and Save refuses it.
		if !part && config.OS != "linux" {
			return nil, fmt.Errorf("layer %s of type %s of manifest %s is not a kernel, an initrd or a ROM: copy the image with Accessor.Copy", layer.Digest, layer.MediaType, desc.Digest)
		}
	}

	return img, nil
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

	tr := tar.NewReader(vr.blob)
	hdr, rr, err := ReadTarFile(tr, target)
	if err != nil {
		return nil, 0, err
	}
	vr.entry, vr.remaining = rr, hdr.Size
	return vr, hdr.Size, nil
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

// ReadTarFile returns the header and the contents of the regular file at
// target in tr. It compares the paths as clean absolute paths.
//
// Unlike [unikraft.com/x/mergefs.Merge], it uses the first entry at
// target, not the last entry, and it does not follow links. A symlink or a
// hard link at target causes an error.
func ReadTarFile(tr *tar.Reader, target string) (*tar.Header, io.Reader, error) {
	target = filepath.Join("/", target)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, err
		}

		if filepath.Join("/", hdr.Name) != target {
			continue
		}

		if t := hdr.Typeflag; t != tar.TypeReg {
			return nil, nil, fmt.Errorf("%s is not a regular file (type %q)", target, t)
		}

		return hdr, tr, nil
	}

	return nil, nil, &fs.PathError{Op: "open", Path: target, Err: fs.ErrNotExist}
}
