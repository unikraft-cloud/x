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
	"os"
	"strings"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
	"unikraft.com/x/log"
)

func SaveContent(ctx context.Context, store content.Ingester, images ...*Image) (ocispec.Descriptor, error) {
	eg, egCtx := errgroup.WithContext(ctx)

	imageLayers := make([][]ocispec.Descriptor, len(images))

	for i, image := range images {
		var layers []ocispec.Descriptor

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

		imageLayers[i] = layers
	}

	if err := eg.Wait(); err != nil {
		return ocispec.Descriptor{}, err
	}

	var mfstDescs []ocispec.Descriptor

	for i, image := range images {
		layers := imageLayers[i]

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

		mfstDescs = append(mfstDescs, mfstDesc)
	}

	idx := ocispec.Index{
		Versioned: ocispecs.Versioned{
			SchemaVersion: 2,
		},
		MediaType: ocispec.MediaTypeImageIndex,
		Manifests: mfstDescs,
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
		// FIXME: try and preserve original digest if content was sourced from a descriptor
		Digest: digest.FromBytes(data),
	}

	log.G(ctx).
		Debug().
		Str("mediaType", mediaType).
		Str("digest", desc.Digest.String()).
		Msg("packaging json")

	w, err := content.OpenWriter(ctx, store, writerOpts(ctx, desc)...)
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

	w, err := content.OpenWriter(ctx, store, writerOpts(ctx, wdesc)...)
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
	defer func() {
		if r != nil {
			r.Close()
		}
	}()

	digester := digest.Canonical.Digester()

	var reader io.Reader

	if path == "" {
		_, err = io.Copy(digester.Hash(), r)
		if err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to buffer layer content: %w", err)
		}
		if seeker, ok := r.(io.Seeker); ok {
			if _, err := seeker.Seek(0, io.SeekStart); err != nil {
				return ocispec.Descriptor{}, fmt.Errorf("failed to rewind layer content: %w", err)
			}
		} else {
			if err := r.Close(); err != nil {
				return ocispec.Descriptor{}, err
			}
			r = nil
			r, _, err = file.Open(ctx)
			if err != nil {
				return ocispec.Descriptor{}, err
			}
		}
		reader = r
	} else {
		tmp, err := os.CreateTemp("", "imagespec-layer-*")
		if err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to create temp layer file: %w", err)
		}
		defer func() {
			tmp.Close()
			os.Remove(tmp.Name())
		}()

		mw := io.MultiWriter(tmp, digester.Hash())
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
		if err := tmp.Sync(); err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to sync layer tar: %w", err)
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to rewind layer content: %w", err)
		}

		stat, err := tmp.Stat()
		if err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to stat layer tar: %w", err)
		}
		size = stat.Size()

		reader = tmp
	}

	desc := ocispec.Descriptor{
		MediaType: mediaType,
		Size:      size,
		Digest:    digester.Digest(),
	}
	w, err := content.OpenWriter(ctx, store, writerOpts(ctx, desc)...)
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

	_, err = io.Copy(w, reader)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to buffer layer content: %w", err)
	}

	err = w.Commit(ctx, desc.Size, desc.Digest)
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
		return ocispec.Descriptor{}, fmt.Errorf("failed to commit layer content: %w", err)
	}

	err = w.Close()
	w = nil
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to finalize layer content write: %w", err)
	}
	return desc, nil
}

func writerOpts(ctx context.Context, desc ocispec.Descriptor) []content.WriterOpt {
	return []content.WriterOpt{
		content.WithDescriptor(desc),
		content.WithRef(remotes.MakeRefKey(ctx, desc)),
	}
}
