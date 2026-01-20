// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"maps"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func NewImage(opts ...NewImageOpt) *Image {
	builder := &imageBuilder{}
	for _, opt := range opts {
		opt(builder)
	}
	return builder.build()
}

type NewImageOpt func(*imageBuilder)

func WithKernel(kernel File) NewImageOpt {
	return func(b *imageBuilder) {
		b.kernel = kernel
	}
}

func WithDebugKernel(kernel File) NewImageOpt {
	return func(b *imageBuilder) {
		b.kernelDebug = kernel
	}
}

func WithInitrd(initrd File) NewImageOpt {
	return func(b *imageBuilder) {
		b.initrd = initrd
	}
}

func WithRom(rom File) NewImageOpt {
	return func(b *imageBuilder) {
		b.roms = append(b.roms, rom)
	}
}

func WithImageConfig(config ocispec.ImageConfig) NewImageOpt {
	return func(b *imageBuilder) {
		b.config = &config
	}
}

func WithImageMetadata(metadata ImageMetadata) NewImageOpt {
	return func(b *imageBuilder) {
		if b.metadata == nil {
			b.metadata = &ImageMetadata{}
		}
		if metadata.KraftkitVersion != "" {
			b.metadata.KraftkitVersion = metadata.KraftkitVersion
		}
		if metadata.Created != nil {
			b.metadata.Created = metadata.Created
		}
		if metadata.Author != "" {
			b.metadata.Author = metadata.Author
		}
	}
}

type imageBuilder struct {
	kernel      File
	kernelDebug File
	initrd      File
	roms        []File

	annotations map[string]string
	config      *ocispec.ImageConfig
	metadata    *ImageMetadata
}

func (builder imageBuilder) build() *Image {
	var img *ocispec.Image
	annotations := maps.Clone(builder.annotations)
	if builder.config != nil {
		img = &ocispec.Image{
			Config: *builder.config,
		}
	}
	if builder.metadata != nil {
		if img == nil {
			img = &ocispec.Image{}
		}
		if annotations == nil {
			annotations = make(map[string]string)
		}
		if builder.metadata.Author != "" {
			img.Author = builder.metadata.Author
			annotations[ocispec.AnnotationAuthors] = builder.metadata.Author
		}
		if builder.metadata.Created != nil {
			img.Created = builder.metadata.Created
			annotations[ocispec.AnnotationCreated] = builder.metadata.Created.Format("2006-01-02T15:04:05Z07:00")
		}
		if builder.metadata.KraftkitVersion != "" {
			annotations[AnnotationKraftKitVersion] = builder.metadata.KraftkitVersion
		}
	}

	return &Image{
		Kernel:      builder.kernel,
		KernelDebug: builder.kernelDebug,
		Initrd:      builder.initrd,
		Roms:        builder.roms,
		Image:       img,
		Annotations: maps.Clone(annotations),
	}
}
