// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/platforms"
	"github.com/distribution/reference"

	"unikraft.com/x/image-spec/schemes"
)

type Accessor struct {
	remote          remotes.Resolver
	registryHosts   docker.RegistryHosts
	registryHeaders http.Header
	refParser       func(string) (reference.Named, error)
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

func WithRegistryHosts(hosts docker.RegistryHosts) AccessOpt {
	return func(so *Accessor) {
		so.registryHosts = hosts
	}
}

func WithRegistryHeaders(headers http.Header) AccessOpt {
	return func(so *Accessor) {
		if headers == nil {
			so.registryHeaders = nil
			return
		}
		so.registryHeaders = headers.Clone()
	}
}

func WithReferenceParser(rp func(string) (reference.Named, error)) AccessOpt {
	return func(so *Accessor) {
		so.refParser = rp
	}
}

func (accessor *Accessor) Load(ctx context.Context, src *Location, platform platforms.MatchComparer) (*Image, error) {
	switch src.Scheme {
	case schemes.OCI:
		named, err := accessor.refParser(src.Path)
		if err != nil {
			return nil, fmt.Errorf("parsing image reference %q: %w", src, err)
		}
		return LoadRegistryImage(ctx, named, accessor.remote, platform)
	case schemes.OCILayout:
		path, tag := splitPathTag(src.Path)
		return LoadOCILayoutNamed(ctx, path, tag, platform)
	case schemes.OCIArchive:
		return LoadTarball(ctx, src.Path, platform)
	default:
		return nil, fmt.Errorf("unsupported location scheme: %q", src.Scheme)
	}
}

func (accessor *Accessor) LoadAll(ctx context.Context, src *Location, platform platforms.MatchComparer) ([]*Image, error) {
	switch src.Scheme {
	case schemes.OCI:
		named, err := accessor.refParser(src.Path)
		if err != nil {
			return nil, fmt.Errorf("parsing image reference %q: %w", src, err)
		}
		return LoadAllRegistryImages(ctx, named, accessor.remote, platform)
	case schemes.OCILayout:
		path, tag := splitPathTag(src.Path)
		return LoadAllOCILayoutsNamed(ctx, path, tag, platform)
	case schemes.OCIArchive:
		return LoadAllTarballs(ctx, src.Path, platform)
	default:
		return nil, fmt.Errorf("unsupported location scheme: %q", src.Scheme)
	}
}

func (accessor *Accessor) Save(ctx context.Context, dest *Location, img ...*Image) error {
	switch dest.Scheme {
	case schemes.OCI:
		named, err := accessor.refParser(dest.Path)
		if err != nil {
			return fmt.Errorf("parsing image reference %q: %w", dest, err)
		}
		_, _, err = SaveRegistryImage(ctx, named, accessor.remote, img...)
		return err
	case schemes.OCILayout:
		path, tag := splitPathTag(dest.Path)
		if tag == "" {
			tag = "latest"
		}
		_, err := SaveOCILayoutNamed(ctx, path, tag, img...)
		return err
	case schemes.OCIArchive:
		return SaveTarball(ctx, dest.Path, img...)
	default:
		return fmt.Errorf("unsupported location scheme: %q", dest.Scheme)
	}
}

func (accessor *Accessor) Delete(ctx context.Context, target *Location) error {
	switch target.Scheme {
	case schemes.OCI:
		if accessor.registryHosts == nil {
			return fmt.Errorf("no registry hosts configured for %q", target.Path)
		}
		named, err := accessor.refParser(target.Path)
		if err != nil {
			return fmt.Errorf("parsing image reference %q: %w", target, err)
		}
		return DeleteRegistryImage(ctx, named, accessor.remote, accessor.registryHosts, accessor.registryHeaders)
	case schemes.OCILayout:
		path, tag := splitPathTag(target.Path)
		if tag == "" {
			return os.RemoveAll(path)
		}
		return DeleteOCILayoutNamed(path, tag)
	case schemes.OCIArchive:
		return os.Remove(target.Path)
	default:
		return fmt.Errorf("unsupported location scheme: %q", target.Scheme)
	}
}
