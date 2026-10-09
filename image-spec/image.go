// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"errors"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"unikraft.com/x/image-spec/reference"
	"unikraft.com/x/image-spec/schemes"
)

// Image represents a unikraft image stored in OCI format.
type Image struct {
	// Reference is the reference the image was requested by (if any).
	Reference reference.Reference
	// ResolvedReference names the image by digest where it was found (if any).
	// For a registry it is Reference without its tag and with its digest; for a
	// layout served over HTTP it is the location the origin redirected to.
	ResolvedReference reference.Reference

	// Descriptor is the OCI descriptor of the image manifest (if available).
	Descriptor ocispec.Descriptor
	// Provider is the content provider for the image (if available).
	Provider content.Provider

	// Image components
	Kernel      File
	KernelDebug File
	Initrd      File
	Roms        []File

	// Image configs
	Image       *ocispec.Image
	Annotations map[string]string

	cleanup []func() error
}

// source returns the registry repository the image's layers can be mounted
// from, or the zero reference if it did not come from a registry.
func (i *Image) source() reference.Reference {
	ref := i.ResolvedReference
	if ref.IsZero() {
		ref = i.Reference
	}
	if ref.Scheme() != schemes.OCI {
		return reference.Reference{}
	}
	return ref
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
