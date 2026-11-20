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

// WithRegistryHeaders sets headers sent with every request to a registry, and
// to every origin serving a layout over HTTP, whatever its host.
func WithRegistryHeaders(headers http.Header) AccessOpt {
	return func(so *Accessor) {
		if headers == nil {
			so.registryHeaders = nil
			return
		}
		so.registryHeaders = headers.Clone()
	}
}

// Resolve pins src to what it currently names, asking the remote: a registry
// for the root its tag names, or an origin serving a layout over HTTP for the
// URL it redirects a tag to. A local layout or archive has nothing to resolve,
// and is returned as given.
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
		resolved, err := registryResolvedReference(named, desc)
		if err != nil {
			return Resolved{}, err
		}
		return Resolved{
			Reference:         requested,
			ResolvedReference: resolved,
			Location:          Location{Scheme: schemes.OCI, Path: canonical.String()},
			Descriptor:        desc,
		}, nil

	case schemes.HTTPOCI, schemes.HTTPSOCI:
		requested, err := reference.Parse(src.String())
		if err != nil {
			return Resolved{}, fmt.Errorf("parsing image reference %q: %w", src, err)
		}
		pinned, err := ResolveTarballRemote(ctx, requested.AsURL(), accessor.httpClient, accessor.registryHeaders)
		if err != nil {
			return Resolved{}, err
		}
		location := Location{Scheme: src.Scheme, Path: pinned.Host + pinned.Path}
		res := Resolved{Reference: requested, Location: location}
		if err := namesDigest(pinned); err == nil {
			res.ResolvedReference, err = reference.Parse(location.String())
			if err != nil {
				return Resolved{}, fmt.Errorf("naming resolved image %q: %w", pinned, err)
			}
		}
		return res, nil

	case schemes.OCILayout, schemes.OCIArchive:
		return Resolved{Location: *src}, nil

	default:
		return Resolved{}, fmt.Errorf("unsupported location scheme: %q", src.Scheme)
	}
}

// Load loads the image res names, from res.Descriptor if it is set, and pins
// the Resolved the image carries to the root it read, so that loading that
// again reads the same content. A registry location without a descriptor is
// resolved first.
func (accessor *Accessor) Load(ctx context.Context, res Resolved, platform platforms.MatchComparer) (*Image, error) {
	res, err := accessor.pin(ctx, res)
	if err != nil {
		return nil, err
	}

	var img *Image
	switch res.Location.Scheme {
	case schemes.OCI:
		var named distref.Named
		named, err = reference.ParseNormalizedNamed(res.Location.Path)
		if err != nil {
			return nil, fmt.Errorf("parsing image reference %q: %w", &res.Location, err)
		}
		img, err = LoadRegistryImage(ctx, named, res.Descriptor, accessor.remote, platform)
	case schemes.HTTPOCI, schemes.HTTPSOCI:
		var ref reference.Reference
		ref, err = reference.Parse(res.Location.String())
		if err != nil {
			return nil, fmt.Errorf("parsing image reference %q: %w", &res.Location, err)
		}
		img, err = LoadTarballRemote(ctx, ref.AsURL(), res.Descriptor, accessor.httpClient, accessor.registryHeaders, platform)
	case schemes.OCILayout:
		path, tag := splitPathTag(res.Location.Path)
		if res.Descriptor.Digest != "" {
			img, err = LoadOCILayout(ctx, path, res.Descriptor, platform)
		} else {
			img, err = LoadOCILayoutNamed(ctx, path, tag, platform)
		}
	case schemes.OCIArchive:
		img, err = LoadTarball(ctx, res.Location.Path, res.Descriptor, platform)
	default:
		return nil, fmt.Errorf("unsupported location scheme: %q", res.Location.Scheme)
	}
	if err != nil {
		return nil, err
	}
	if res.Descriptor.Digest == "" {
		res.Descriptor = img.Descriptor
	}
	img.Resolved = res
	return img, nil
}

// LoadAll loads all images a Resolve or Save pinned, as Load does.
func (accessor *Accessor) LoadAll(ctx context.Context, res Resolved, platform platforms.MatchComparer) ([]*Image, error) {
	res, err := accessor.pin(ctx, res)
	if err != nil {
		return nil, err
	}

	var imgs []*Image
	switch res.Location.Scheme {
	case schemes.OCI:
		var named distref.Named
		named, err = reference.ParseNormalizedNamed(res.Location.Path)
		if err != nil {
			return nil, fmt.Errorf("parsing image reference %q: %w", &res.Location, err)
		}
		imgs, err = LoadAllRegistryImages(ctx, named, res.Descriptor, accessor.remote, platform)
	case schemes.HTTPOCI, schemes.HTTPSOCI:
		var ref reference.Reference
		ref, err = reference.Parse(res.Location.String())
		if err != nil {
			return nil, fmt.Errorf("parsing image reference %q: %w", &res.Location, err)
		}
		imgs, err = LoadAllTarballsRemote(ctx, ref.AsURL(), res.Descriptor, accessor.httpClient, accessor.registryHeaders, platform)
	case schemes.OCILayout:
		path, tag := splitPathTag(res.Location.Path)
		if res.Descriptor.Digest != "" {
			imgs, err = LoadAllOCILayouts(ctx, path, res.Descriptor, platform)
		} else {
			imgs, err = LoadAllOCILayoutsNamed(ctx, path, tag, platform)
		}
	case schemes.OCIArchive:
		imgs, err = LoadAllTarballs(ctx, res.Location.Path, res.Descriptor, platform)
	default:
		return nil, fmt.Errorf("unsupported location scheme: %q", res.Location.Scheme)
	}
	if err != nil {
		return nil, err
	}
	for _, img := range imgs {
		pinned := res
		if pinned.Descriptor.Digest == "" {
			pinned.Descriptor = img.Descriptor
		}
		img.Resolved = pinned
	}
	return imgs, nil
}

// pin resolves a registry location that has no descriptor yet, since a
// registry can only be loaded from by descriptor.
func (accessor *Accessor) pin(ctx context.Context, res Resolved) (Resolved, error) {
	if res.Location.Scheme != schemes.OCI || res.Descriptor.Digest != "" {
		return res, nil
	}
	return accessor.Resolve(ctx, &res.Location)
}

// Save stores img at dest under one index, and returns dest pinned to the
// root it wrote, so that Load reads back exactly what was saved. For an
// archive, that root is the archive's own index.json, which lists the index.
func (accessor *Accessor) Save(ctx context.Context, dest *Location, img ...*Image) (Resolved, error) {
	switch dest.Scheme {
	case schemes.OCI:
		named, err := reference.ParseNormalizedNamed(dest.Path)
		if err != nil {
			return Resolved{}, fmt.Errorf("parsing image reference %q: %w", dest, err)
		}
		named, desc, err := SaveRegistryImage(ctx, named, accessor.remote, img...)
		if err != nil {
			return Resolved{}, err
		}
		requested, err := reference.FromNamed(named)
		if err != nil {
			return Resolved{}, fmt.Errorf("parsing image reference %q: %w", dest, err)
		}
		canonical, err := distref.WithDigest(distref.TrimNamed(named), desc.Digest)
		if err != nil {
			return Resolved{}, fmt.Errorf("saved image %q has an invalid digest: %w", named, err)
		}
		resolved, err := registryResolvedReference(named, desc)
		if err != nil {
			return Resolved{}, err
		}
		return Resolved{
			Reference:         requested,
			ResolvedReference: resolved,
			Location:          Location{Scheme: schemes.OCI, Path: canonical.String()},
			Descriptor:        desc,
		}, nil
	case schemes.OCILayout:
		path, tag := splitPathTag(dest.Path)
		if tag == "" {
			tag = "latest"
		}
		desc, err := SaveOCILayoutNamed(ctx, path, tag, img...)
		if err != nil {
			return Resolved{}, err
		}
		return Resolved{Location: *dest, Descriptor: desc}, nil
	case schemes.OCIArchive:
		desc, err := SaveTarball(ctx, dest.Path, img...)
		if err != nil {
			return Resolved{}, err
		}
		return Resolved{Location: *dest, Descriptor: desc}, nil
	default:
		return Resolved{}, fmt.Errorf("unsupported location scheme: %q", dest.Scheme)
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
