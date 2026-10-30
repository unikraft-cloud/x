// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"context"
	"fmt"

	distref "github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/image-spec/reference"
	"unikraft.com/x/image-spec/schemes"
	"unikraft.com/x/log"

	"unikraft.com/x/build/progress"
)

// Saved tells where Save stored images.
type Saved struct {
	Ref       string
	Digest    digest.Digest
	Platforms []ocispec.Platform
}

// Save stores images at dest under one index and reports where they are. A
// registry destination is pushed through the Images accessor, which holds
// the credentials.
func (b Builder) Save(ctx context.Context, dest *imagespec.Location, images ...*imagespec.Image) (Saved, error) {
	saved := Saved{Ref: dest.Path, Platforms: make([]ocispec.Platform, 0, len(images))}
	for _, img := range images {
		var p ocispec.Platform
		if img.Image != nil {
			p = img.Image.Platform
		}
		saved.Platforms = append(saved.Platforms, p)
	}

	// Only a registry needs the accessor, for its resolver and credentials.
	access := imagespec.NewAccessor()
	verb := "save"
	if dest.Scheme == schemes.OCI {
		verb = "push"
		named, err := reference.ParseNormalizedNamed(dest.Path)
		if err != nil {
			return Saved{}, fmt.Errorf("parsing image reference %q: %w", dest.Path, err)
		}
		saved.Ref = distref.TagNameOnly(named).String()
		if access, err = b.images(ctx); err != nil {
			return Saved{}, err
		}
	}

	var res imagespec.Resolved
	err := progress.Step(progress.WithPrefix(b.progressWriter(), verb), saved.Ref, func() (err error) {
		res, err = access.Save(ctx, dest, images...)
		return err
	})
	if err != nil {
		return Saved{}, err
	}
	saved.Digest = res.Descriptor.Digest

	log.G(ctx).Debug().
		Str("ref", saved.Ref).
		Str("digest", saved.Digest.String()).
		Msg("saved images")

	return saved, nil
}
