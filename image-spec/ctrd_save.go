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
	"os"
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
	for _, image := range images {
		if image.skippedLayers {
			return nil, fmt.Errorf("image %s has layers that are not Unikraft parts, which a save cannot write: copy it with Accessor.Copy", image.ManifestDescriptor.Digest)
		}
		if image.Compression != CompressionNone && image.Compression != CompressionZstd {
			return nil, fmt.Errorf("image %s: unknown compression %q", image.ManifestDescriptor.Digest, image.Compression)
		}
	}
	store = ingestRef(store, ref)

	eg, egCtx := errgroup.WithContext(ctx)

	imageLayers := make([][]*savedLayer, len(images))

	for i, image := range images {
		var layers []*savedLayer

		// Kernel layer
		if image.Kernel != nil {
			layer := new(savedLayer)
			layers = append(layers, layer)
			eg.Go(func() error {
				kernelDesc, diffID, err := packageLayer(egCtx, store, image, image.Kernel, ocispec.MediaTypeImageLayer, WellKnownKernelPath, image.Compression)
				if err != nil {
					return fmt.Errorf("failed to package kernel: %w", err)
				}
				kernelDesc.Annotations = maps.Clone(kernelDesc.Annotations) // a copied layer has its source's map
				if kernelDesc.Annotations == nil {
					kernelDesc.Annotations = make(map[string]string)
				}
				kernelDesc.Annotations[AnnotationKernelPath] = WellKnownKernelPath
				*layer = savedLayer{kernelDesc, diffID}
				return nil
			})
		}

		// Kernel debug layer
		if image.KernelDebug != nil {
			layer := new(savedLayer)
			layers = append(layers, layer)
			eg.Go(func() error {
				kernelDesc, diffID, err := packageLayer(egCtx, store, image, image.KernelDebug, ocispec.MediaTypeImageLayer, WellKnownKernelDbgPath, image.Compression)
				if err != nil {
					return fmt.Errorf("failed to package kernel: %w", err)
				}
				kernelDesc.Annotations = maps.Clone(kernelDesc.Annotations)
				if kernelDesc.Annotations == nil {
					kernelDesc.Annotations = make(map[string]string)
				}
				kernelDesc.Annotations[AnnotationKernelDbgPath] = WellKnownKernelDbgPath
				*layer = savedLayer{kernelDesc, diffID}
				return nil
			})
		}

		// Initrd layer
		if image.Initrd != nil {
			layer := new(savedLayer)
			layers = append(layers, layer)
			eg.Go(func() error {
				initrdDesc, diffID, err := packageLayer(egCtx, store, image, image.Initrd, ocispec.MediaTypeImageLayer, WellKnownInitrdPath, image.Compression)
				if err != nil {
					return fmt.Errorf("failed to package initrd: %w", err)
				}
				initrdDesc.Annotations = maps.Clone(initrdDesc.Annotations)
				if initrdDesc.Annotations == nil {
					initrdDesc.Annotations = make(map[string]string)
				}
				initrdDesc.Annotations[AnnotationKernelInitrdPath] = WellKnownInitrdPath
				*layer = savedLayer{initrdDesc, diffID}
				return nil
			})
		}

		// ROM layers
		for _, rom := range image.Roms {
			layer := new(savedLayer)
			layers = append(layers, layer)
			eg.Go(func() error {
				romDesc, diffID, err := packageLayer(egCtx, store, image, rom, MediaTypeRom, "", image.Compression)
				if err != nil {
					return fmt.Errorf("failed to package rom: %w", err)
				}
				*layer = savedLayer{romDesc, diffID}
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
		diffIDs := make([]digest.Digest, len(imageLayers[i]))
		for j, layer := range imageLayers[i] {
			layers[j], diffIDs[j] = layer.desc, layer.diffID
		}

		// Image
		ociImage := ocispec.Image{}
		if image.Image != nil {
			ociImage = *image.Image
		}
		ociImage.RootFS.Type = "layers"
		ociImage.RootFS.DiffIDs = diffIDs

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

// savedLayer is a layer of a save and the digest of its uncompressed content.
type savedLayer struct {
	desc   ocispec.Descriptor
	diffID digest.Digest
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
	if _, err := copyBlob(ctx, store, source, input, desc, false, ""); err != nil {
		return ocispec.Descriptor{}, err
	}
	return desc, nil
}

// copyBlob is packageCopy, and if wantDiffID is set, it returns the DiffID of desc: from a copy, which decompresses
// a compressed layer, or else from hint or a read of input, as store already has desc. It reads input at most once.
func copyBlob(ctx context.Context, store content.Ingester, source reference.Reference, input content.Provider, desc ocispec.Descriptor, wantDiffID bool, hint digest.Digest) (digest.Digest, error) {
	// A pusher puts the digest in its requests before it reads the blob.
	if err := desc.Digest.Validate(); err != nil {
		return "", fmt.Errorf("blob %q: %w", desc.Digest, err)
	}
	compressed := wantDiffID && isCompressedLayer(desc.MediaType)
	var diffID digest.Digest
	if wantDiffID {
		diffID = desc.Digest
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
		switch {
		case compressed && hint != "":
			return hint, nil
		case compressed:
			return layerDiffID(ctx, input, desc, maxLayerSize)
		}
		return diffID, nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to create content writer: %w", err)
	}
	defer func() {
		if w != nil {
			w.Close()
		}
	}()

	if err := restart(w); err != nil {
		return "", err
	}

	// A registry pusher resets its writer when it re-sends the upload: start over with a new reader.
	for attempt := 0; ; attempt++ {
		// NOTE: an ingester need not check what it commits: a pusher leaves that
		// to the registry it pushes to.
		var vr *verifiedReader
		vr, err = openVerified(ctx, input, desc)
		if err != nil {
			return "", err
		}
		var src io.Reader = vr
		var dw *diffIDWriter
		if compressed {
			dw = newDiffIDWriter(ctx, desc, maxLayerSize)
			src = io.TeeReader(vr, dw)
		}
		_, err = io.Copy(w, src)
		vr.Close()
		if dw != nil {
			var derr error
			if diffID, derr = dw.finish(err); derr != nil {
				return "", derr
			}
		}
		if errors.Is(err, content.ErrReset) && attempt < maxResets {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("failed to copy content: %w", err)
		}
		err = w.Commit(ctx, desc.Size, desc.Digest)
		if !errors.Is(err, content.ErrReset) || attempt == maxResets {
			break
		}
	}
	if err != nil && !errdefs.IsAlreadyExists(err) {
		return "", fmt.Errorf("failed to commit content: %w", err)
	}
	err = w.Close()
	w = nil
	if err != nil {
		return "", fmt.Errorf("failed to finalize content write: %w", err)
	}

	log.G(ctx).Debug().
		Str("digest", desc.Digest.String()).
		Msg("copied layer")

	return diffID, nil
}

// maxResets bounds the writes again after a reset: containerd re-sends at most that often.
const maxResets = 4

func packageLayer(ctx context.Context, store content.Ingester, image *Image, file File, mediaType string, path string, compression Compression) (_ ocispec.Descriptor, diffID digest.Digest, rerr error) {
	if desc, provider := file.Source(); desc.Digest != "" && store != nil {
		// if we have a source descriptor and provider, copy directly
		id, err := copyBlob(ctx, store, image.source(), provider, desc, true, diffIDHint(file))
		if err != nil {
			return ocispec.Descriptor{}, "", err
		}
		return desc, id, nil
	}

	log.G(ctx).
		Debug().
		Str("mediaType", mediaType).
		Str("path", path).
		Msg("packaging layer")

	r, size, err := file.Open(ctx)
	if err != nil {
		return ocispec.Descriptor{}, "", err
	}
	defer func() {
		if r != nil {
			r.Close()
		}
	}()

	digester := digest.Canonical.Digester()
	// diffIDDigester hashes the uncompressed content when the layer is compressed.
	var diffIDDigester digest.Digester

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
			return ocispec.Descriptor{}, "", fmt.Errorf("failed to write layer tar header: %w", err)
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

	// tmp holds a compressed layer, as its digest covers the compressed bytes.
	var tmp *os.File

	// rewind takes reader back to its start for another write.
	rewind := func() error {
		if tmp != nil {
			if _, err := tmp.Seek(0, io.SeekStart); err != nil {
				return fmt.Errorf("failed to rewind layer content: %w", err)
			}
			reader = tmp
			return nil
		}
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
	if path != "" && compression == CompressionZstd && mediaType == ocispec.MediaTypeImageLayer {
		tmp, err = os.CreateTemp("", "imagespec-layer-*")
		if err != nil {
			return ocispec.Descriptor{}, "", fmt.Errorf("failed to create temp layer file: %w", err)
		}
		defer func() {
			tmp.Close()
			os.Remove(tmp.Name())
		}()
		zw, err := newZstdWriter(io.MultiWriter(tmp, digester.Hash()))
		if err != nil {
			return ocispec.Descriptor{}, "", fmt.Errorf("failed to compress layer: %w", err)
		}
		diffIDDigester = digest.Canonical.Digester()
		n, err := io.Copy(io.MultiWriter(zw, diffIDDigester.Hash()), reader)
		if err != nil {
			zw.Close()
			return ocispec.Descriptor{}, "", fmt.Errorf("failed to read layer content: %w", err)
		}
		if n != want {
			zw.Close()
			return ocispec.Descriptor{}, "", fmt.Errorf("layer tar is %d bytes, expected %d: file size changed", n, want)
		}
		if err := zw.Close(); err != nil {
			return ocispec.Descriptor{}, "", fmt.Errorf("failed to compress layer: %w", err)
		}
		// The writes read tmp, so close the source now.
		err = r.Close()
		r = nil
		if err != nil {
			return ocispec.Descriptor{}, "", fmt.Errorf("failed to read layer content: %w", err)
		}
		if size, err = tmp.Seek(0, io.SeekCurrent); err != nil {
			return ocispec.Descriptor{}, "", fmt.Errorf("failed to size compressed layer: %w", err)
		}
		mediaType = ocispec.MediaTypeImageLayerZstd
	} else {
		size, err = io.Copy(digester.Hash(), reader)
		if err != nil {
			return ocispec.Descriptor{}, "", fmt.Errorf("failed to read layer content: %w", err)
		}
		if path != "" && size != want {
			return ocispec.Descriptor{}, "", fmt.Errorf("layer tar is %d bytes, expected %d: file size changed", size, want)
		}
	}
	if err := rewind(); err != nil {
		return ocispec.Descriptor{}, "", err
	}

	desc := ocispec.Descriptor{
		MediaType: mediaType,
		Size:      size,
		Digest:    digester.Digest(),
	}
	diffID = desc.Digest
	if diffIDDigester != nil {
		diffID = diffIDDigester.Digest()
	}
	w, err := content.OpenWriter(ctx, store, content.WithDescriptor(desc))
	if errdefs.IsAlreadyExists(err) {
		log.G(ctx).Debug().
			Str("mediaType", mediaType).
			Str("path", path).
			Str("digest", desc.Digest.String()).
			Msg("packaged pre-existing layer")
		return desc, diffID, nil
	} else if err != nil {
		return ocispec.Descriptor{}, "", fmt.Errorf("failed to create layer content writer: %w", err)
	}
	defer func() {
		if w != nil {
			w.Close()
		}
	}()
	if err := restart(w); err != nil {
		return ocispec.Descriptor{}, "", err
	}

	// A registry pusher resets its writer when it re-sends the upload, so
	// start over, as content.Copy does.
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if err := rewind(); err != nil {
				return ocispec.Descriptor{}, "", err
			}
		}
		_, err = io.Copy(w, reader)
		if errors.Is(err, content.ErrReset) && attempt < maxResets {
			continue
		}
		if err != nil {
			return ocispec.Descriptor{}, "", fmt.Errorf("failed to buffer layer content: %w", err)
		}
		err = w.Commit(ctx, desc.Size, desc.Digest)
		if !errors.Is(err, content.ErrReset) || attempt == maxResets {
			break
		}
	}
	if err == nil {
		log.G(ctx).Debug().
			Str("mediaType", mediaType).
			Str("path", path).
			Str("digest", desc.Digest.String()).
			Msg("packaged layer")
	} else if errdefs.IsAlreadyExists(err) {
		log.G(ctx).Debug().
			Str("mediaType", mediaType).
			Str("path", path).
			Str("digest", desc.Digest.String()).
			Msg("packaged pre-existing layer")
	} else {
		return ocispec.Descriptor{}, "", fmt.Errorf("failed to commit layer content: %w", err)
	}

	err = w.Close()
	w = nil
	if err != nil {
		return ocispec.Descriptor{}, "", fmt.Errorf("failed to finalize layer content write: %w", err)
	}
	return desc, diffID, nil
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

// openVerified returns a verified reader of desc, from the data that desc embeds or else from input.
func openVerified(ctx context.Context, input content.Provider, desc ocispec.Descriptor) (*verifiedReader, error) {
	var r content.ReaderAt
	if inline := inlineData(desc); inline != nil {
		r = inlineReaderAt{bytes.NewReader(inline)}
	} else {
		var err error
		if r, err = input.ReaderAt(ctx, desc); err != nil {
			return nil, fmt.Errorf("failed to get reader for descriptor: %w", err)
		}
	}
	vr, err := newVerifiedReader(r, desc)
	if err != nil {
		r.Close()
		return nil, err
	}
	return vr, nil
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
