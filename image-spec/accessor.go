// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"context"
	"fmt"

	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/platforms"
	"github.com/distribution/reference"
)

type Accessor struct {
	remote    remotes.Resolver
	refParser func(string) (reference.Named, error)
}

func NewAccessor(opts ...AccessOpt) *Accessor {
	s := &Accessor{}
	for _, o := range opts {
		o(s)
	}
	if s.refParser == nil {
		s.refParser = reference.ParseNormalizedNamed
	}
	if s.remote == nil {
		s.remote = docker.NewResolver(docker.ResolverOptions{})
	}
	return s
}

type AccessOpt func(*Accessor)

func WithResolver(r remotes.Resolver) AccessOpt {
	return func(so *Accessor) {
		so.remote = r
	}
}

func WithReferenceParser(rp func(string) (reference.Named, error)) AccessOpt {
	return func(so *Accessor) {
		so.refParser = rp
	}
}

func (accessor *Accessor) Load(ctx context.Context, src *URI, platform platforms.MatchComparer) (*Image, error) {
	switch src.Scheme {
	case URISchemeOCI:
		named, err := accessor.refParser(src.Path)
		if err != nil {
			return nil, fmt.Errorf("parsing image reference %q: %w", src, err)
		}
		return LoadDockerImage(ctx, named, accessor.remote, platform)
	case URISchemeOCILayout:
		path, tag := parsePathTag(src.Path)
		return LoadOCILayoutNamed(ctx, path, tag, platform)
	case URISchemeOCIArchive:
		return LoadTarball(ctx, src.Path, platform)
	default:
		return nil, fmt.Errorf("unsupported URI scheme: %q", src.Scheme)
	}
}

func (accessor *Accessor) LoadAll(ctx context.Context, src *URI, platform platforms.MatchComparer) ([]*Image, error) {
	switch src.Scheme {
	case URISchemeOCI:
		named, err := accessor.refParser(src.Path)
		if err != nil {
			return nil, fmt.Errorf("parsing image reference %q: %w", src, err)
		}
		return LoadAllDockerImages(ctx, named, accessor.remote, platform)
	case URISchemeOCILayout:
		path, tag := parsePathTag(src.Path)
		return LoadAllOCILayoutsNamed(ctx, path, tag, platform)
	case URISchemeOCIArchive:
		return LoadAllTarballs(ctx, src.Path, platform)
	default:
		return nil, fmt.Errorf("unsupported URI scheme: %q", src.Scheme)
	}
}

func (accessor *Accessor) Save(ctx context.Context, dest *URI, img ...*Image) error {
	switch dest.Scheme {
	case URISchemeOCI:
		named, err := accessor.refParser(dest.Path)
		if err != nil {
			return fmt.Errorf("parsing image reference %q: %w", dest, err)
		}
		_, _, err = SaveDockerImage(ctx, named, accessor.remote, img...)
		return err
	case URISchemeOCILayout:
		path, tag := parsePathTag(dest.Path)
		if tag == "" {
			tag = "latest"
		}
		_, err := SaveOCILayoutNamed(ctx, path, tag, img...)
		return err
	case URISchemeOCIArchive:
		return SaveTarball(ctx, dest.Path, img...)
	default:
		return fmt.Errorf("unsupported URI scheme: %q", dest.Scheme)
	}
}
