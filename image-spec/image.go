// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"errors"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Image represents a unikraft image stored in OCI format.
type Image struct {
	// Resolved is where the image was loaded from (if available).
	Resolved
	// ManifestDescriptor is the OCI descriptor of the image manifest it was
	// loaded from (if available).
	ManifestDescriptor ocispec.Descriptor

	// Image components
	Kernel      File
	KernelDebug File
	Initrd      File
	Roms        []File

	// Compression is how Save compresses the new tar layers of the parts. Save copies a part that has a source without change.
	// Load sets CompressionZstd if one or more of the tar layers of the parts are zstd, and CompressionNone if not.
	Compression Compression

	// Image configs
	Image            *ocispec.Image
	Annotations      map[string]string
	IndexAnnotations map[string]string

	skippedLayers bool // the load left out layers that are not Unikraft parts
	cleanup       []func() error
}

func (i *Image) Close() error {
	var err error
	for _, cleanup := range i.cleanup {
		err = errors.Join(err, cleanup())
	}
	if i.Kernel != nil {
		err = errors.Join(err, i.Kernel.Cleanup())
	}
	if i.KernelDebug != nil {
		err = errors.Join(err, i.KernelDebug.Cleanup())
	}
	if i.Initrd != nil {
		err = errors.Join(err, i.Initrd.Cleanup())
	}
	for _, rom := range i.Roms {
		err = errors.Join(err, rom.Cleanup())
	}
	return err
}

type ImageMetadata struct {
	KraftkitVersion string

	Author  string
	Created *time.Time
}

func (i *Image) Metadata() ImageMetadata {
	metadata := ImageMetadata{}

	if i.Image != nil {
		metadata.Author = i.Image.Author
		metadata.Created = i.Image.Created
	}
	if i.Annotations != nil {
		if v, ok := i.Annotations[AnnotationKraftKitVersion]; ok {
			metadata.KraftkitVersion = v
		}

		if metadata.Author == "" {
			if v, ok := i.Annotations[ocispec.AnnotationAuthors]; ok {
				metadata.Author = v
			}
		}
		if metadata.Created == nil {
			if v, ok := i.Annotations[ocispec.AnnotationCreated]; ok {
				if t, err := time.Parse(time.RFC3339, v); err == nil {
					metadata.Created = &t
				}
			}
		}
	}

	return metadata
}
