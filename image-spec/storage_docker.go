// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"context"
	"fmt"

	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/distribution/reference"
	"github.com/moby/buildkit/util/contentutil"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// LoadDockerImage loads a Docker image from a remote registry.
func LoadDockerImage(ctx context.Context, named reference.Named, remote remotes.Resolver) (*Image, error) {
	named = reference.TagNameOnly(named)

	name, desc, err := remote.Resolve(ctx, named.String())
	if err != nil {
		return nil, fmt.Errorf("failed to resolve image %q: %w", named, err)
	}

	fetcher, err := remote.Fetcher(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to get fetcher for image %q: %w", named, err)
	}
	provider := contentutil.FromFetcher(fetcher)

	img, err := loadCtrdImage(ctx, provider, desc)
	if err != nil {
		return nil, fmt.Errorf("failed to load image %q: %w", named, err)
	}
	img.Name = named
	return img, nil
}

// SaveDockerImage saves a Docker image to a remote registry.
func SaveDockerImage(ctx context.Context, named reference.Named, remote remotes.Resolver, image *Image) (reference.Named, ocispec.Descriptor, error) {
	named = reference.TagNameOnly(named)

	pusher, err := remote.Pusher(ctx, named.String())
	if err != nil {
		return nil, ocispec.Descriptor{}, fmt.Errorf("failed to get pusher for image %q: %w", named, err)
	}
	ingester := contentutil.FromPusher(pusher)

	desc, err := saveCtrdImage(ctx, ingester, named.Name(), image)
	if err != nil {
		return nil, ocispec.Descriptor{}, fmt.Errorf("failed to save image %q: %w", named, err)
	}
	return named, desc, nil
}
