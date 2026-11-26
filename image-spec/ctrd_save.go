// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"

	"unikraft.com/x/image-spec/reference"
	"unikraft.com/x/log"
)

func SaveContent(ctx context.Context, store content.Ingester, ref string, images ...*Image) (ocispec.Descriptor, error) {
	mfstDescs, err := saveManifests(ctx, store, ref, images...)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	store = ingestRef(store, ref)

	idxAnnotations := map[string]string{}
	for _, image := range images {
		maps.Copy(idxAnnotations, image.IndexAnnotations)
	}
	idx := ocispec.Index{
		SchemaVersion: 2,
		MediaType:     ocispec.MediaTypeImageIndex,
		Manifests:     mfstDescs,
		Annotations:   idxAnnotations,
	}
	idxDesc, err := packageJSON(ctx, store, idx.MediaType, idx)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to package index: %w", err)
	}

	return idxDesc, nil
}

// SaveManifest saves image to store as a manifest, without an index, and
// returns its index entry.
func SaveManifest(ctx context.Context, store content.Ingester, ref string, image *Image) (ocispec.Descriptor, error) {
	descs, err := saveManifests(ctx, store, ref, image)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	return descs[0], nil
}

func saveManifests(ctx context.Context, store content.Ingester, ref string, images ...*Image) ([]ocispec.Descriptor, error) {
	store = ingestRef(store, ref)

	eg, egCtx := errgroup.WithContext(ctx)

	imageLayers := make([][]*ocispec.Descriptor, len(images))

	for i, image := range images {
		var layers []*ocispec.Descriptor

		// Kernel layer
		if image.Kernel != nil {
			layer := new(ocispec.Descriptor)
			layers = append(layers, layer)
			eg.Go(func() error {
				kernelDesc, err := packageLayer(egCtx, store, image, image.Kernel, ocispec.MediaTypeImageLayer, WellKnownKernelPath)
				if err != nil {
					return fmt.Errorf("failed to package kernel: %w", err)
				}
				kernelDesc.Annotations = maps.Clone(kernelDesc.Annotations) // a copied layer has its source's map
				if kernelDesc.Annotations == nil {
					kernelDesc.Annotations = make(map[string]string)
				}
				kernelDesc.Annotations[AnnotationKernelPath] = WellKnownKernelPath
				*layer = kernelDesc
				return nil
			})
		}

		// Kernel debug layer
		if image.KernelDebug != nil {
			layer := new(ocispec.Descriptor)
			layers = append(layers, layer)
			eg.Go(func() error {
				kernelDesc, err := packageLayer(egCtx, store, image, image.KernelDebug, ocispec.MediaTypeImageLayer, WellKnownKernelDbgPath)
				if err != nil {
					return fmt.Errorf("failed to package kernel: %w", err)
				}
				kernelDesc.Annotations = maps.Clone(kernelDesc.Annotations)
				if kernelDesc.Annotations == nil {
					kernelDesc.Annotations = make(map[string]string)
				}
				kernelDesc.Annotations[AnnotationKernelDbgPath] = WellKnownKernelDbgPath
				*layer = kernelDesc
				return nil
			})
		}

		// Initrd layer
		if image.Initrd != nil {
			layer := new(ocispec.Descriptor)
			layers = append(layers, layer)
			eg.Go(func() error {
				initrdDesc, err := packageLayer(egCtx, store, image, image.Initrd, ocispec.MediaTypeImageLayer, WellKnownInitrdPath)
				if err != nil {
					return fmt.Errorf("failed to package initrd: %w", err)
				}
				initrdDesc.Annotations = maps.Clone(initrdDesc.Annotations)
				if initrdDesc.Annotations == nil {
					initrdDesc.Annotations = make(map[string]string)
				}
				initrdDesc.Annotations[AnnotationKernelInitrdPath] = WellKnownInitrdPath
				*layer = initrdDesc
				return nil
			})
		}

		// ROM layers
		for _, rom := range image.Roms {
			layer := new(ocispec.Descriptor)
			layers = append(layers, layer)
			eg.Go(func() error {
				romDesc, err := packageLayer(egCtx, store, image, rom, MediaTypeRom, "")
				if err != nil {
					return fmt.Errorf("failed to package rom: %w", err)
				}
				*layer = romDesc
				return nil
			})
		}

		imageLayers[i] = layers
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	var mfstDescs []ocispec.Descriptor

	for i, image := range images {
		layers := make([]ocispec.Descriptor, len(imageLayers[i]))
		for j, layer := range imageLayers[i] {
			layers[j] = *layer
		}

		// Image
		ociImage := ocispec.Image{}
		if image.Image != nil {
			ociImage = *image.Image
		}
		ociImage.RootFS.Type = "layers"
		ociImage.RootFS.DiffIDs = make([]digest.Digest, 0, len(layers))
		for _, layer := range layers {
			// NOTE: layers are uncompressed, so DiffID == Digest
			ociImage.RootFS.DiffIDs = append(ociImage.RootFS.DiffIDs, layer.Digest)
		}

		var err error
		imgDesc, err := packageJSON(ctx, store, ocispec.MediaTypeImageConfig, &ociImage)
		if err != nil {
			return nil, fmt.Errorf("failed to package image config: %w", err)
		}

		mfst := ocispec.Manifest{
			SchemaVersion: 2,
			MediaType:     ocispec.MediaTypeImageManifest,
			Config:        imgDesc,
			Layers:        layers,
			Annotations:   image.Annotations,
		}
		mfstDesc, err := packageJSON(ctx, store, mfst.MediaType, mfst)
		if err != nil {
			return nil, fmt.Errorf("failed to package manifest: %w", err)
		}
		mfstDesc.Annotations = image.Annotations
		if image.Image != nil {
			// NOTE: ECR rejects index descriptors with os.features; they remain in the config.
			platform := image.Image.Platform
			platform.OSFeatures = nil
			mfstDesc.Platform = &platform
		}

		mfstDescs = append(mfstDescs, mfstDesc)
	}

	return mfstDescs, nil
}

func packageJSON(ctx context.Context, store content.Ingester, mediaType string, dt any) (ocispec.Descriptor, error) {
	if dt == nil {
		dt = struct{}{}
	}
	data, err := json.MarshalIndent(dt, "", "  ")
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	desc := ocispec.Descriptor{
		MediaType: mediaType,
		Size:      int64(len(data)),
		// FIXME: try and preserve original digest if content was sourced from a descriptor
		Digest: digest.FromBytes(data),
	}

	log.G(ctx).
		Debug().
		Str("mediaType", mediaType).
		Str("digest", desc.Digest.String()).
		Msg("packaging json")

	w, err := content.OpenWriter(ctx, store, content.WithDescriptor(desc))
	if errdefs.IsAlreadyExists(err) {
		return desc, nil
	}
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	defer w.Close()

	err = content.Copy(ctx, w, bytes.NewReader(data), int64(len(data)), desc.Digest)
	if err != nil {
		return ocispec.Descriptor{}, err
	}

	log.G(ctx).Debug().
		Str("mediaType", mediaType).
		Str("digest", desc.Digest.String()).
		Msg("packaged json")

	return desc, nil
}

// packageCopy copies desc from input to store, and lets a pusher mount it from the registry repository source.
func packageCopy(ctx context.Context, store content.Ingester, source reference.Reference, input content.Provider, desc ocispec.Descriptor) (ocispec.Descriptor, error) {
	// A pusher puts the digest in its requests before it reads the blob.
	if err := desc.Digest.Validate(); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("blob %q: %w", desc.Digest, err)
	}
	log.G(ctx).
		Debug().
		Str("digest", desc.Digest.String()).
		Msg("copying layer")

	// A pusher mounts a blob from the repositories of its source labels: keep only that of source.
	wdesc := desc
	wdesc.Annotations = maps.Clone(desc.Annotations)
	maps.DeleteFunc(wdesc.Annotations, func(k, _ string) bool {
		return strings.HasPrefix(k, labels.LabelDistributionSource)
	})
	if !source.IsZero() {
		if wdesc.Annotations == nil {
			wdesc.Annotations = make(map[string]string)
		}
		// containerd looks the label up by hostname, without the port.
		host := (&url.URL{Host: source.Domain()}).Hostname()
		wdesc.Annotations[labels.LabelDistributionSource+"."+host] = source.Path()
	}

	w, err := content.OpenWriter(ctx, store, content.WithDescriptor(wdesc))
	if errdefs.IsAlreadyExists(err) {
		log.G(ctx).Debug().
			Str("digest", wdesc.Digest.String()).
			Msg("layer already exists, skipping copy")
		return desc, nil
	}
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to create content writer: %w", err)
	}
	defer func() {
		if w != nil {
			w.Close()
		}
	}()

	if err := restart(w); err != nil {
		return ocispec.Descriptor{}, err
	}
	var r content.ReaderAt
	if inline := inlineData(desc); inline != nil {
		r = inlineReaderAt{bytes.NewReader(inline)}
	} else if r, err = input.ReaderAt(ctx, desc); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to get reader for descriptor: %w", err)
	}
	// NOTE: an ingester need not check what it commits: a pusher leaves that
	// to the registry it pushes to.
	vr, err := newVerifiedReader(r, desc)
	if err != nil {
		r.Close()
		return ocispec.Descriptor{}, err
	}
	defer vr.Close()
	if err := content.Copy(ctx, w, vr, desc.Size, desc.Digest); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to copy content: %w", err)
	}
	err = w.Close()
	w = nil
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to finalize content write: %w", err)
	}

	log.G(ctx).Debug().
		Str("digest", desc.Digest.String()).
		Msg("copied layer")

	return desc, nil
}

func packageLayer(ctx context.Context, store content.Ingester, image *Image, file File, mediaType string, path string) (_ ocispec.Descriptor, rerr error) {
	if desc, provider := file.Source(); desc.Digest != "" && store != nil {
		// if we have a source descriptor and provider, copy directly
		return packageCopy(ctx, store, image.source(), provider, desc)
	}

	log.G(ctx).
		Debug().
		Str("mediaType", mediaType).
		Str("path", path).
		Msg("packaging layer")

	r, size, err := file.Open(ctx)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	defer func() {
		if r != nil {
			r.Close()
		}
	}()

	digester := digest.Canonical.Digester()

	// The tar of one file is its header, the file, then zeros: the padding of
	// the file to a block and two empty blocks. No copy of the file is kept.
	var header, trailer []byte
	if path != "" {
		var buf bytes.Buffer
		err = tar.NewWriter(&buf).WriteHeader(&tar.Header{
			Name: strings.TrimPrefix(path, "/"),
			Mode: 0o644,
			Size: size,
		})
		if err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to write layer tar header: %w", err)
		}
		header = buf.Bytes()
		trailer = make([]byte, (512-size%512)%512+1024)
	}

	var reader io.Reader
	layer := func() {
		reader = r
		if path != "" {
			reader = io.MultiReader(bytes.NewReader(header), r, bytes.NewReader(trailer))
		}
	}

	// rewind takes reader back to its start for another write.
	rewind := func() error {
		if seeker, ok := r.(io.Seeker); ok {
			if _, err := seeker.Seek(0, io.SeekStart); err != nil {
				return fmt.Errorf("failed to rewind layer content: %w", err)
			}
			layer()
			return nil
		}
		if err := r.Close(); err != nil {
			return err
		}
		r = nil
		var err error
		r, _, err = file.Open(ctx)
		layer()
		return err
	}

	layer()
	want := int64(len(header)) + size + int64(len(trailer))
	size, err = io.Copy(digester.Hash(), reader)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to read layer content: %w", err)
	}
	if path != "" && size != want {
		return ocispec.Descriptor{}, fmt.Errorf("layer tar is %d bytes, expected %d: file size changed", size, want)
	}
	if err := rewind(); err != nil {
		return ocispec.Descriptor{}, err
	}

	desc := ocispec.Descriptor{
		MediaType: mediaType,
		Size:      size,
		Digest:    digester.Digest(),
	}
	w, err := content.OpenWriter(ctx, store, content.WithDescriptor(desc))
	if errdefs.IsAlreadyExists(err) {
		log.G(ctx).Debug().
			Str("mediaType", mediaType).
			Str("path", path).
			Str("digest", desc.Digest.String()).
			Msg("packaged pre-existing layer")
		return desc, nil
	} else if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to create layer content writer: %w", err)
	}
	defer func() {
		if w != nil {
			w.Close()
		}
	}()
	if err := restart(w); err != nil {
		return ocispec.Descriptor{}, err
	}
	layerReader := &rereader{Reader: reader, open: func() (io.Reader, error) { return reader, rewind() }}
	if err := content.Copy(ctx, w, layerReader, desc.Size, desc.Digest); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to write layer content: %w", err)
	}
	log.G(ctx).Debug().
		Str("mediaType", mediaType).
		Str("path", path).
		Str("digest", desc.Digest.String()).
		Msg("packaged layer")

	err = w.Close()
	w = nil
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to finalize layer content write: %w", err)
	}
	return desc, nil
}

// rereader reads what open gives again from the start on a seek to 0, as
// content.Copy does when a pusher resets its upload.
type rereader struct {
	io.Reader
	open func() (io.Reader, error)
}

func (r *rereader) Seek(offset int64, whence int) (int64, error) {
	if offset != 0 || whence != io.SeekStart {
		return 0, errors.New("layer content can only be read again from its start")
	}
	var err error
	r.Reader, err = r.open()
	return 0, err
}

// restart truncates what an earlier write left under the ref of w, which a local store resumes.
func restart(w content.Writer) error {
	st, err := w.Status()
	if err != nil {
		return fmt.Errorf("failed to get content write status: %w", err)
	}
	if st.Offset > 0 {
		if err := w.Truncate(0); err != nil {
			return fmt.Errorf("failed to reset content write: %w", err)
		}
	}
	return nil
}

// inlineData returns the data that desc embeds if it matches desc, as content.ReadBlob reads it, or nil.
func inlineData(desc ocispec.Descriptor) []byte {
	if len(desc.Data) == 0 || int64(len(desc.Data)) != desc.Size || desc.Digest.Validate() != nil ||
		desc.Digest.Algorithm().FromBytes(desc.Data) != desc.Digest {
		return nil
	}
	return desc.Data
}

// inlineReaderAt is a content.ReaderAt of the data that a descriptor embeds.
type inlineReaderAt struct{ *bytes.Reader }

func (inlineReaderAt) Close() error { return nil }

// ingestRef returns store with a writer per ref and digest, so blobs are written in parallel.
func ingestRef(store content.Ingester, ref string) content.Ingester {
	return refIngester{store, ref}
}

type refIngester struct {
	content.Ingester
	ref string
}

func (r refIngester) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	var wOpts content.WriterOpts
	for _, opt := range opts {
		if err := opt(&wOpts); err != nil {
			return nil, err
		}
	}
	ref := r.ref
	if wOpts.Desc.Digest != "" {
		ref += "@" + wOpts.Desc.Digest.String()
	}
	return r.Ingester.Writer(ctx, append(slices.Clone(opts), content.WithRef(ref))...)
}
