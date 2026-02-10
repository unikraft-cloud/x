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
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
	"unikraft.com/x/log"
)

func SaveContent(ctx context.Context, store content.Ingester, ref string, image *Image) (ocispec.Descriptor, error) {
	store = ingestDefaults(store, content.WithRef(ref))

	eg, egCtx := errgroup.WithContext(ctx)
	layers := []ocispec.Descriptor{}

	// Kernel layer
	if image.Kernel != nil {
		idx := len(layers)
		layers = append(layers, ocispec.Descriptor{})
		eg.Go(func() error {
			kernelDesc, err := packageLayer(egCtx, store, image, image.Kernel, ocispec.MediaTypeImageLayer, WellKnownKernelPath)
			if err != nil {
				return fmt.Errorf("failed to package kernel: %w", err)
			}
			if kernelDesc.Annotations == nil {
				kernelDesc.Annotations = make(map[string]string)
			}
			kernelDesc.Annotations[AnnotationKernelPath] = WellKnownKernelPath
			layers[idx] = kernelDesc
			return nil
		})
	}

	// Kernel debug layer
	if image.KernelDebug != nil {
		idx := len(layers)
		layers = append(layers, ocispec.Descriptor{})
		eg.Go(func() error {
			kernelDesc, err := packageLayer(egCtx, store, image, image.KernelDebug, ocispec.MediaTypeImageLayer, WellKnownKernelDbgPath)
			if err != nil {
				return fmt.Errorf("failed to package kernel: %w", err)
			}
			if kernelDesc.Annotations == nil {
				kernelDesc.Annotations = make(map[string]string)
			}
			kernelDesc.Annotations[AnnotationKernelDbgPath] = WellKnownKernelDbgPath
			layers[idx] = kernelDesc
			return nil
		})
	}

	// Initrd layer
	if image.Initrd != nil {
		idx := len(layers)
		layers = append(layers, ocispec.Descriptor{})
		eg.Go(func() error {
			initrdDesc, err := packageLayer(egCtx, store, image, image.Initrd, ocispec.MediaTypeImageLayer, WellKnownInitrdPath)
			if err != nil {
				return fmt.Errorf("failed to package initrd: %w", err)
			}
			if initrdDesc.Annotations == nil {
				initrdDesc.Annotations = make(map[string]string)
			}
			initrdDesc.Annotations[AnnotationKernelInitrdPath] = WellKnownInitrdPath
			layers[idx] = initrdDesc
			return nil
		})
	}

	// ROM layers
	for _, rom := range image.Roms {
		idx := len(layers)
		layers = append(layers, ocispec.Descriptor{})
		eg.Go(func() error {
			romDesc, err := packageLayer(egCtx, store, image, rom, MediaTypeRom, "")
			if err != nil {
				return fmt.Errorf("failed to package rom: %w", err)
			}
			layers[idx] = romDesc
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return ocispec.Descriptor{}, err
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
		return ocispec.Descriptor{}, fmt.Errorf("failed to package image config: %w", err)
	}

	mfst := ocispec.Manifest{
		Versioned: ocispecs.Versioned{
			SchemaVersion: 2,
		},
		MediaType:   ocispec.MediaTypeImageManifest,
		Config:      imgDesc,
		Layers:      layers,
		Annotations: image.Annotations,
	}
	mfstDesc, err := packageJSON(ctx, store, mfst.MediaType, mfst)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to package manifest: %w", err)
	}
	mfstDesc.Annotations = image.Annotations
	mfstDesc.Platform = &image.Image.Platform

	idx := ocispec.Index{
		Versioned: ocispecs.Versioned{
			SchemaVersion: 2,
		},
		MediaType: ocispec.MediaTypeImageIndex,
		Manifests: []ocispec.Descriptor{mfstDesc},
	}
	idxDesc, err := packageJSON(ctx, store, idx.MediaType, idx)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to package index: %w", err)
	}

	return idxDesc, nil
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
		Digest:    digest.FromBytes(data),
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

	// if the image source has a name, add a cross-repo mount
	wdesc := desc
	if image.Name != nil {
		if wdesc.Annotations == nil {
			wdesc.Annotations = make(map[string]string)
		} else {
			wdesc.Annotations = maps.Clone(wdesc.Annotations)
		}
		source := reference.Domain(image.Name)
		repo := reference.Path(image.Name)
		wdesc.Annotations[labels.LabelDistributionSource+"."+source] = repo
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

	r, err := input.ReaderAt(ctx, desc)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to get reader for descriptor: %w", err)
	}

	err = content.Copy(ctx, w, content.NewReader(r), desc.Size, desc.Digest)
	if err != nil {
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
		return packageCopy(ctx, store, image, provider, desc)
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
	defer r.Close()

	w, err := content.OpenWriter(ctx, store)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to create layer content writer: %w", err)
	}
	defer func() {
		if w != nil {
			w.Close()
		}
	}()

	digester := digest.Canonical.Digester()
	counter := &writeCounter{}
	mw := io.MultiWriter(w, digester.Hash(), counter)

	if path == "" {
		_, err = io.Copy(mw, r)
		if err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to buffer layer content: %w", err)
		}
	} else {
		tw := tar.NewWriter(mw)
		err = tw.WriteHeader(&tar.Header{
			Name: strings.TrimPrefix(path, "/"),
			Mode: 0o644,
			Size: size,
		})
		if err != nil {
			tw.Close()
			return ocispec.Descriptor{}, fmt.Errorf("failed to write layer tar header: %w", err)
		}
		_, err = io.Copy(tw, r)
		if err != nil {
			tw.Close()
			return ocispec.Descriptor{}, fmt.Errorf("failed to buffer layer content: %w", err)
		}
		if err := tw.Close(); err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to finalize layer tar: %w", err)
		}
	}

	desc := ocispec.Descriptor{
		MediaType: mediaType,
		Size:      counter.Size(),
		Digest:    digester.Digest(),
	}
	if err := w.Commit(ctx, desc.Size, desc.Digest); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to commit layer content: %w", err)
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

type writeCounter struct {
	n int64
}

func (wc *writeCounter) Write(p []byte) (int, error) {
	n := len(p)
	wc.n += int64(n)
	return n, nil
}

func (wc *writeCounter) Size() int64 {
	return wc.n
}

func ingestDefaults(store content.Ingester, opts ...content.WriterOpt) content.Ingester {
	return defaultIngester{store, opts}
}

type defaultIngester struct {
	content.Ingester
	opts []content.WriterOpt
}

func (r defaultIngester) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	allOpts := append(slices.Clone(r.opts), opts...)
	return r.Ingester.Writer(ctx, allOpts...)
}
