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
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
)

func LoadContent(ctx context.Context, store content.Provider, desc ocispec.Descriptor, platform platforms.MatchComparer) (*Image, error) {
	mfsts, err := imageManifests(ctx, store, desc, platform)
	if err != nil {
		return nil, err
	}
	annotations, err := indexAnnotations(ctx, store, desc)
	if err != nil {
		return nil, err
	}
	img, err := loadCtrdImageMfst(ctx, store, desc, mfsts[0])
	if err != nil {
		return nil, err
	}
	img.IndexAnnotations = annotations
	return img, nil
}

func LoadAllContent(ctx context.Context, store content.Provider, desc ocispec.Descriptor, platform platforms.MatchComparer) ([]*Image, error) {
	mfsts, err := imageManifests(ctx, store, desc, platform)
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
			img, err := loadCtrdImageMfst(ctx, store, desc, mfst)
			if err != nil {
				return fmt.Errorf("failed to load image manifest: %w", err)
			}
			img.IndexAnnotations = maps.Clone(annotations)
			imgs[i] = img
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
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

// loadCtrdImageMfst loads the image whose manifest is desc, recording root as
// what it was loaded from.
func loadCtrdImageMfst(ctx context.Context, store content.Provider, root ocispec.Descriptor, desc ocispec.Descriptor) (*Image, error) {
	mfst, err := readManifest(ctx, store, desc)
	if err != nil {
		return nil, fmt.Errorf("failed to read image manifest: %w", err)
	}
	img := &Image{
		Descriptor:         root,
		ManifestDescriptor: desc,
		Annotations:        mfst.Annotations,
	}

	dt, err := readVerifiedBlob(ctx, store, mfst.Config, maxConfigSize)
	if err != nil {
		return nil, fmt.Errorf("failed to read image config blob: %w", err)
	}
	var config ocispec.Image
	if err := json.Unmarshal(dt, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal image config blob: %w", err)
	}
	img.Image = &config

	for i, layer := range mfst.Layers {
		// A save computes a DiffID with digest.Canonical, so it takes only a hint with that algorithm.
		// The digest of a compressed layer is never its DiffID, so it is no hint.
		var hint digest.Digest
		if isCompressedLayer(layer.MediaType) && len(config.RootFS.DiffIDs) == len(mfst.Layers) {
			if id := config.RootFS.DiffIDs[i]; id.Validate() == nil && id.Algorithm() == digest.Canonical && id != layer.Digest {
				hint = id
			}
		}
		file := func(p string) *ContentStoreFile {
			f := NewContentStoreFile(store, layer, p)
			f.diffID, f.hintFor = hint, layer.Digest
			return f
		}
		part := true
		switch layer.MediaType {
		// Classic OCI layer (old-style image layer), which can be compressed.
		case ocispec.MediaTypeImageLayer, images.MediaTypeDockerSchema2Layer, ocispec.MediaTypeImageLayerGzip,
			ocispec.MediaTypeImageLayerZstd, images.MediaTypeDockerSchema2LayerGzip:
			part = false
			if p := layer.Annotations[AnnotationKernelPath]; p != "" {
				img.Kernel = file(p)
				part = true
			}
			if p := layer.Annotations[AnnotationKernelDbgPath]; p != "" {
				img.KernelDebug = file(p)
				part = true
			}
			if p := layer.Annotations[AnnotationKernelInitrdPath]; p != "" {
				img.Initrd = file(p)
				part = true
			}
			if part && layer.MediaType == ocispec.MediaTypeImageLayerZstd {
				img.Compression = CompressionZstd
			}

		case MediaTypeRom:
			img.Roms = append(img.Roms, NewContentStoreFile(store, layer, ""))

		default:
			part = false
		}
		if part {
			continue
		}
		// A Unikraft image, or an old one without an OS, has only parts.
		// Another image loads without its other layers, for the rootfs of a build, and Save refuses it.
		if slices.Contains([]string{"", PlatformKraftcloud, PlatformFirecracker, PlatformQemu}, config.OS) {
			return nil, fmt.Errorf("layer %s of type %s of manifest %s is not a kernel, an initrd or a ROM: copy the image with Accessor.Copy", layer.Digest, layer.MediaType, desc.Digest)
		}
		img.skippedLayers = true
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

	maxSize int64         // the limit of the decompressed bytes that Open reads, maxLayerSize if 0
	diffID  digest.Digest // the DiffID that the config of the source gives, unchecked
	hintFor digest.Digest // the layer that diffID applies to
}

// diffIDHint returns the DiffID that the config of the source of file gives for its layer, or "" if there is none.
// A save uses it only if the destination has the layer already, as it then does not read the layer.
func diffIDHint(file File) digest.Digest {
	if f, ok := file.(*ContentStoreFile); ok && f.hintFor == f.Desc.Digest {
		return f.diffID
	}
	return ""
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
	// The stream does not contain the holes of a sparse entry, so apply the limit and ctx to the entry also.
	if hdr.Size > limit {
		blob.Close()
		return nil, 0, fmt.Errorf("%w: %s is %d bytes, more than %d", ErrLayerTooLarge, target, hdr.Size, limit)
	}
	vr.entry, vr.remaining = &boundedReader{ctx: ctx, r: rr, limit: limit}, hdr.Size
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
	c.maxSize, c.diffID, c.hintFor = f.maxSize, f.diffID, f.hintFor
	return c, nil
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
