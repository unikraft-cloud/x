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
	"golang.org/x/sync/semaphore"

	"unikraft.com/x/log"
)

// SaveOpt sets an option of a save.
type SaveOpt func(*saveOptions)

// saveOptions are the options of a save.
type saveOptions struct {
	compression Compression
}

// WithCompression sets how a save compresses the layers that it packages for the parts of an image.
func WithCompression(compression Compression) SaveOpt {
	return func(o *saveOptions) {
		o.compression = compression
	}
}

// SaveContent saves image to store under one index.
func SaveContent(ctx context.Context, store content.Ingester, ref string, image *Image, opts ...SaveOpt) (ocispec.Descriptor, error) {
	return SaveAllContent(ctx, store, ref, []*Image{image}, opts...)
}

// SaveAllContent saves images to store under one index.
func SaveAllContent(ctx context.Context, store content.Ingester, ref string, images []*Image, opts ...SaveOpt) (ocispec.Descriptor, error) {
	mfstDescs, err := saveManifests(ctx, store, ref, images, opts...)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	store = ingestRef(store, ref)

	idxAnnotations := map[string]string{}
	for _, image := range images {
		maps.Copy(idxAnnotations, image.IndexAnnotations)
	}
	if src := sourceIndex(images, mfstDescs, idxAnnotations); src != nil {
		if err := content.WriteBlob(ctx, store, src.desc.Digest.String(), bytes.NewReader(src.raw), src.desc); err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to copy image index: %w", err)
		}
		return src.desc, nil
	}
	// NOTE: Harbor rejects an index entry without a platform.
	for i, image := range images {
		if mfstDescs[i].Platform == nil {
			mfstDescs[i].Platform = entryPlatform(image)
		}
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
func SaveManifest(ctx context.Context, store content.Ingester, ref string, image *Image, opts ...SaveOpt) (ocispec.Descriptor, error) {
	descs, err := saveManifests(ctx, store, ref, []*Image{image}, opts...)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	return descs[0], nil
}

func saveManifests(ctx context.Context, store content.Ingester, ref string, images []*Image, opts ...SaveOpt) ([]ocispec.Descriptor, error) {
	var o saveOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.compression != CompressionNone && o.compression != CompressionZstd {
		return nil, fmt.Errorf("unknown compression %q", o.compression)
	}
	store = ingestRef(store, ref)

	eg, egCtx := errgroup.WithContext(ctx)

	imageLayers := make([][]*savedLayer, len(images))
	copies := semaphore.NewWeighted(maxBlobCopies)

	for i, image := range images {
		if image.byManifest() {
			if image.manifest != nil {
				eg.Go(func() error {
					return copyBlobs(egCtx, store, image, copies)
				})
			}
			continue
		}

		var layers []*savedLayer

		// Kernel layer
		if image.Kernel != nil {
			layer := new(savedLayer)
			layers = append(layers, layer)
			eg.Go(func() error {
				kernelDesc, diffID, err := packageLayer(egCtx, store, image, image.Kernel, ocispec.MediaTypeImageLayer, WellKnownKernelPath, o.compression)
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
				kernelDesc, diffID, err := packageLayer(egCtx, store, image, image.KernelDebug, ocispec.MediaTypeImageLayer, WellKnownKernelDbgPath, o.compression)
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
				initrdDesc, diffID, err := packageLayer(egCtx, store, image, image.Initrd, ocispec.MediaTypeImageLayer, WellKnownInitrdPath, o.compression)
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
				romDesc, diffID, err := packageLayer(egCtx, store, image, rom, MediaTypeRom, "", o.compression)
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
		// An image without parts is the manifest it names, copied after all blobs: a pusher puts each manifest to the tag.
		if image.byManifest() {
			if image.manifest != nil {
				desc := image.ManifestDescriptor
				if err := content.WriteBlob(ctx, store, desc.Digest.String(), bytes.NewReader(image.manifest), desc); err != nil {
					return nil, fmt.Errorf("failed to copy image manifest: %w", err)
				}
			}
			mfstDescs = append(mfstDescs, image.ManifestDescriptor)
			continue
		}

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
		mfstDesc.Platform = entryPlatform(image)

		mfstDescs = append(mfstDescs, mfstDesc)
	}

	return mfstDescs, nil
}

// entryPlatform returns the platform of the index entry of image, from its config, or nil if the config has none.
func entryPlatform(image *Image) *ocispec.Platform {
	if image.Image == nil || image.Image.OS == "" || image.Image.Architecture == "" {
		return nil
	}
	// NOTE: ECR rejects index descriptors with os.features; they remain in the config.
	platform := image.Image.Platform
	platform.OSFeatures = nil
	return &platform
}

// savedLayer is a layer of a save and the digest of its uncompressed content.
type savedLayer struct {
	desc   ocispec.Descriptor
	diffID digest.Digest
}

// maxBlobCopies bounds the blob copies of one save that run at the same time.
const maxBlobCopies = 8

// copyBlobs copies the config and the layers of a loaded image to store.
func copyBlobs(ctx context.Context, store content.Ingester, image *Image, copies *semaphore.Weighted) error {
	var mfst ocispec.Manifest
	if err := json.Unmarshal(image.manifest, &mfst); err != nil {
		return fmt.Errorf("failed to read image manifest: %w", err)
	}
	eg, egCtx := errgroup.WithContext(ctx)
	blobs, err := copyableBlobs(mfst)
	if err != nil {
		return err
	}
	for _, blob := range blobs {
		eg.Go(func() error {
			if err := copies.Acquire(egCtx, 1); err != nil {
				return err
			}
			defer copies.Release(1)
			if _, err := packageCopy(egCtx, store, image, image.provider, blob); err != nil {
				return fmt.Errorf("failed to copy blob %s: %w", blob.Digest, err)
			}
			return nil
		})
	}
	return eg.Wait()
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

func packageCopy(ctx context.Context, store content.Ingester, image *Image, input content.Provider, desc ocispec.Descriptor) (ocispec.Descriptor, error) {
	log.G(ctx).
		Debug().
		Str("digest", desc.Digest.String()).
		Msg("copying layer")

	// A pusher mounts a blob from the repositories of its source labels, so
	// drop the labels of the manifest, and add a cross-repo mount from the
	// registry that the image came from.
	wdesc := desc
	wdesc.Annotations = maps.Clone(desc.Annotations)
	maps.DeleteFunc(wdesc.Annotations, func(k, _ string) bool {
		return strings.HasPrefix(k, labels.LabelDistributionSource)
	})
	if source := image.source(); !source.IsZero() {
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

	// A local store resumes an earlier write of the blob under its ref, which starts over here.
	st, err := w.Status()
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to get content write status: %w", err)
	}
	if st.Offset > 0 {
		if err := w.Truncate(0); err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to reset content write: %w", err)
		}
	}

	// A registry pusher resets its writer when it re-sends the upload, so
	// start over with a new reader.
	inline := inlineData(desc)
	for attempt := 0; ; attempt++ {
		var r content.ReaderAt
		if inline != nil {
			r = inlineReaderAt{bytes.NewReader(inline)}
		} else if r, err = input.ReaderAt(ctx, desc); err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to get reader for descriptor: %w", err)
		}
		// NOTE: an ingester need not check what it commits: a pusher leaves that
		// to the registry it pushes to.
		var vr *verifiedReader
		vr, err = newVerifiedReader(r, desc)
		if err != nil {
			r.Close()
			return ocispec.Descriptor{}, err
		}
		_, err = io.Copy(w, vr)
		vr.Close()
		if errors.Is(err, content.ErrReset) && attempt < maxResets {
			continue
		}
		if err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to copy content: %w", err)
		}
		err = w.Commit(ctx, desc.Size, desc.Digest)
		if !errors.Is(err, content.ErrReset) || attempt == maxResets {
			break
		}
	}
	if err != nil && !errdefs.IsAlreadyExists(err) {
		return ocispec.Descriptor{}, fmt.Errorf("failed to commit content: %w", err)
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

// maxResets bounds the writes again after a reset: containerd re-sends at most that often.
const maxResets = 4

func packageLayer(ctx context.Context, store content.Ingester, image *Image, file File, mediaType string, path string, compression Compression) (_ ocispec.Descriptor, diffID digest.Digest, rerr error) {
	if desc, provider := file.Source(); desc.Digest != "" && store != nil {
		// if we have a source descriptor and provider, copy directly
		copied, err := packageCopy(ctx, store, image, provider, desc)
		if err != nil {
			return ocispec.Descriptor{}, "", err
		}
		id, err := layerDiffID(ctx, file, provider, copied)
		return copied, id, err
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
	// A local store resumes an earlier write of the blob under its ref, which starts over here.
	st, err := w.Status()
	if err != nil {
		return ocispec.Descriptor{}, "", fmt.Errorf("failed to get layer content write status: %w", err)
	}
	if st.Offset > 0 {
		if err := w.Truncate(0); err != nil {
			return ocispec.Descriptor{}, "", fmt.Errorf("failed to reset layer content write: %w", err)
		}
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

// inlineData returns the data that desc embeds if it matches the size and the digest of desc, as content.ReadBlob reads it, or nil.
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

// ingestRef returns store with the writers of a save to ref, each under ref and the digest of its blob.
func ingestRef(store content.Ingester, ref string) content.Ingester {
	return refIngester{store, ref}
}

// refIngester gives each blob a ref of its own, so that blobs are written in parallel, and keeps saves to other refs apart.
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
