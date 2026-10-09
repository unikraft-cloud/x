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
	distref "github.com/distribution/reference"

	"unikraft.com/x/image-spec/reference"
	"unikraft.com/x/image-spec/schemes"
)

type Accessor struct {
	remote          remotes.Resolver
	registryHosts   docker.RegistryHosts
	registryHeaders http.Header
	httpClient      *http.Client
}

func NewAccessor(opts ...AccessOpt) *Accessor {
	s := &Accessor{}
	for _, o := range opts {
		o(s)
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

// WithHTTPClient sets the client a layout served over HTTP is fetched with.
// Headers the origin requires belong on its transport.
func WithHTTPClient(client *http.Client) AccessOpt {
	return func(so *Accessor) {
		so.httpClient = client
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

// Resolve pins src to the digest it currently names. A registry is asked
// once, so Load does not have to ask again; a local layout or archive is on
// disk already and is returned as given.
func (accessor *Accessor) Resolve(ctx context.Context, src *Location) (Resolved, error) {
	switch src.Scheme {
	case schemes.OCI:
		named, err := reference.ParseNormalizedNamed(src.Path)
		if err != nil {
			return Resolved{}, fmt.Errorf("parsing image reference %q: %w", src, err)
		}
		requested, err := reference.FromNamed(named)
		if err != nil {
			return Resolved{}, fmt.Errorf("parsing image reference %q: %w", src, err)
		}
		canonical, desc, err := ResolveRegistryImage(ctx, named, accessor.remote)
		if err != nil {
			return Resolved{}, err
		}
		resolved, err := reference.FromNamed(canonical)
		if err != nil {
			return Resolved{}, fmt.Errorf("naming resolved image %q: %w", canonical, err)
		}
		return Resolved{
			Reference:         requested,
			ResolvedReference: resolved,
			Location:          Location{Scheme: schemes.OCI, Path: canonical.String()},
			descriptor:        desc,
		}, nil

	case schemes.HTTPOCI, schemes.HTTPSOCI:
		requested, err := reference.Parse(src.String())
		if err != nil {
			return Resolved{}, fmt.Errorf("parsing image reference %q: %w", src, err)
		}
		pinned, err := ResolveTarballRemote(ctx, requested.AsURL(), accessor.httpClient)
		if err != nil {
			return Resolved{}, err
		}
		location := Location{Scheme: src.Scheme, Path: pinned.Host + pinned.Path}
		resolved, err := reference.Parse(location.String())
		if err != nil {
			return Resolved{}, fmt.Errorf("naming resolved image %q: %w", pinned, err)
		}
		return Resolved{
			Reference:         requested,
			ResolvedReference: resolved,
			Location:          location,
		}, nil

	case schemes.OCILayout, schemes.OCIArchive:
		return Resolved{Location: *src}, nil

	default:
		return Resolved{}, fmt.Errorf("unsupported location scheme: %q", src.Scheme)
	}
}

// Load loads the image a Resolve pinned.
func (accessor *Accessor) Load(ctx context.Context, res Resolved, platform platforms.MatchComparer) (*Image, error) {
	var (
		img *Image
		err error
	)
	switch res.Location.Scheme {
	case schemes.OCI:
		var named distref.Named
		named, err = reference.ParseNormalizedNamed(res.Location.Path)
		if err != nil {
			return nil, fmt.Errorf("parsing image reference %q: %w", &res.Location, err)
		}
		img, err = LoadRegistryImage(ctx, named, res.descriptor, accessor.remote, platform)
	case schemes.HTTPOCI, schemes.HTTPSOCI:
		var ref reference.Reference
		ref, err = reference.Parse(res.Location.String())
		if err != nil {
			return nil, fmt.Errorf("parsing image reference %q: %w", &res.Location, err)
		}
		img, err = LoadTarballRemote(ctx, ref.AsURL(), accessor.httpClient, platform)
	case schemes.OCILayout:
		path, tag := splitPathTag(res.Location.Path)
		img, err = LoadOCILayoutNamed(ctx, path, tag, platform)
	case schemes.OCIArchive:
		img, err = LoadTarball(ctx, res.Location.Path, platform)
	default:
		return nil, fmt.Errorf("unsupported location scheme: %q", res.Location.Scheme)
	}
	if err != nil {
		return nil, err
	}
	img.Resolved = res
	return img, nil
}

// LoadAll loads all images a Resolve pinned.
func (accessor *Accessor) LoadAll(ctx context.Context, res Resolved, platform platforms.MatchComparer) ([]*Image, error) {
	var (
		imgs []*Image
		err  error
	)
	switch res.Location.Scheme {
	case schemes.OCI:
		var named distref.Named
		named, err = reference.ParseNormalizedNamed(res.Location.Path)
		if err != nil {
			return nil, fmt.Errorf("parsing image reference %q: %w", &res.Location, err)
		}
		imgs, err = LoadAllRegistryImages(ctx, named, res.descriptor, accessor.remote, platform)
	case schemes.HTTPOCI, schemes.HTTPSOCI:
		var ref reference.Reference
		ref, err = reference.Parse(res.Location.String())
		if err != nil {
			return nil, fmt.Errorf("parsing image reference %q: %w", &res.Location, err)
		}
		imgs, err = LoadAllTarballsRemote(ctx, ref.AsURL(), accessor.httpClient, platform)
	case schemes.OCILayout:
		path, tag := splitPathTag(res.Location.Path)
		imgs, err = LoadAllOCILayoutsNamed(ctx, path, tag, platform)
	case schemes.OCIArchive:
		imgs, err = LoadAllTarballs(ctx, res.Location.Path, platform)
	default:
		return nil, fmt.Errorf("unsupported location scheme: %q", res.Location.Scheme)
	}
	if err != nil {
		return nil, err
	}
	for _, img := range imgs {
		img.Resolved = res
	}
	return imgs, nil
}

func (accessor *Accessor) Save(ctx context.Context, dest *Location, img ...*Image) error {
	switch dest.Scheme {
	case schemes.OCI:
		named, err := reference.ParseNormalizedNamed(dest.Path)
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
		named, err := reference.ParseNormalizedNamed(target.Path)
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
