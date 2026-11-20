// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	distref "github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"

	"unikraft.com/x/image-spec/contentutil"
	"unikraft.com/x/image-spec/reference"
	"unikraft.com/x/image-spec/schemes"
)

// maxBlobCopies bounds the blobs that one copy writes at the same time.
const maxBlobCopies = 8

// Copy copies the image of src to dest blob by blob, so every digest stays, and pins dest to the root; platform (nil for all) selects the manifests of an index, which is copied whole.
func (accessor *Accessor) Copy(ctx context.Context, src Resolved, dest *Location, platform platforms.MatchComparer) (_ Resolved, rerr error) {
	src, err := accessor.pin(ctx, src)
	if err != nil {
		return Resolved{}, err
	}
	provider, root, cleanup, err := accessor.open(ctx, src)
	if err != nil {
		return Resolved{}, err
	}
	defer func() {
		rerr = errors.Join(rerr, cleanup())
	}()

	switch dest.Scheme {
	case schemes.OCI:
		named, err := reference.ParseNormalizedNamed(dest.Path)
		if err != nil {
			return Resolved{}, fmt.Errorf("parsing image reference %q: %w", dest, err)
		}
		named = distref.TagNameOnly(named)
		pusher, err := accessor.remote.Pusher(ctx, named.String())
		if err != nil {
			return Resolved{}, fmt.Errorf("failed to get pusher for image %q: %w", named, err)
		}
		store := ingestRef(contentutil.FromPusher(pusher), named.Name())
		root, err := copyContent(ctx, provider, store, src.source(), root, platform)
		if err != nil {
			return Resolved{}, fmt.Errorf("failed to copy image to %q: %w", named, err)
		}
		return savedRegistryImage(dest, named, root)
	case schemes.OCILayout:
		path, tag := splitPathTag(dest.Path)
		if tag == "" {
			tag = "latest"
		}
		root, err := copyToOCILayout(ctx, path, tag, provider, src.source(), root, platform)
		if err != nil {
			return Resolved{}, err
		}
		return Resolved{Location: *dest, Descriptor: root}, nil
	case schemes.OCIArchive:
		desc, err := copyToTarball(ctx, dest.Path, provider, src.source(), root, platform)
		if err != nil {
			return Resolved{}, err
		}
		return Resolved{Location: *dest, Descriptor: desc}, nil
	default:
		return Resolved{}, fmt.Errorf("unsupported location scheme: %q", dest.Scheme)
	}
}

// open returns the content and the root of the image that res pins, and the removal of what it fetched.
func (accessor *Accessor) open(ctx context.Context, res Resolved) (content.Provider, ocispec.Descriptor, func() error, error) {
	switch res.Location.Scheme {
	case schemes.OCI:
		named, err := reference.ParseNormalizedNamed(res.Location.Path)
		if err != nil {
			return nil, ocispec.Descriptor{}, nil, fmt.Errorf("parsing image reference %q: %w", &res.Location, err)
		}
		provider, err := registryProvider(ctx, named, res.Descriptor, accessor.remote)
		if err != nil {
			return nil, ocispec.Descriptor{}, nil, err
		}
		return provider, res.Descriptor, func() error { return nil }, nil
	case schemes.HTTPOCI, schemes.HTTPSOCI:
		ref, err := reference.Parse(res.Location.String())
		if err != nil {
			return nil, ocispec.Descriptor{}, nil, fmt.Errorf("parsing image reference %q: %w", &res.Location, err)
		}
		body, err := openTarballRemote(ctx, ref.AsURL(), accessor.httpClient, accessor.registryHeaders)
		if err != nil {
			return nil, ocispec.Descriptor{}, nil, err
		}
		defer body.Close()
		return openTarball(ctx, body, res.Descriptor)
	case schemes.OCILayout:
		path, tag := splitPathTag(res.Location.Path)
		return openOCILayout(path, tag, res.Descriptor)
	case schemes.OCIArchive:
		f, err := os.Open(res.Location.Path)
		if err != nil {
			return nil, ocispec.Descriptor{}, nil, fmt.Errorf("failed to open tarball: %w", err)
		}
		defer f.Close()
		return openTarball(ctx, f, res.Descriptor)
	default:
		return nil, ocispec.Descriptor{}, nil, fmt.Errorf("unsupported location scheme: %q", res.Location.Scheme)
	}
}

// copyContent copies root and its tree from provider to store, each blob before its parents, as Copy does, and returns the root it wrote.
func copyContent(ctx context.Context, provider content.Provider, store content.Ingester, source reference.Reference, root ocispec.Descriptor, platform platforms.MatchComparer) (ocispec.Descriptor, error) {
	var walked []ocispec.Descriptor
	kept := map[digest.Digest][]ocispec.Descriptor{} // the entries of each index that platform selects
	children := images.ChildrenHandler(provider)
	handler := images.HandlerFunc(func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		// A registry serves no foreign layer.
		if images.IsNonDistributable(desc.MediaType) {
			return nil, images.ErrSkipDesc
		}
		walked = append(walked, desc)
		descs, err := children(ctx, desc)
		if err != nil {
			return nil, err
		}
		if images.IsIndexType(desc.MediaType) {
			if platform != nil {
				descs = slices.DeleteFunc(slices.Clone(descs), func(d ocispec.Descriptor) bool {
					return d.Platform != nil && !platform.Match(*d.Platform)
				})
				if len(descs) == 0 {
					return nil, fmt.Errorf("no manifest of index %s matches the platform: %w", desc.Digest, errdefs.ErrNotFound)
				}
			}
			kept[desc.Digest] = descs
			return descs, nil
		}
		if !images.IsManifestType(desc.MediaType) {
			return descs, nil
		}
		// A pusher puts a manifest to the tag, so a blob must not be one.
		for _, d := range descs {
			if images.IsManifestType(d.MediaType) || images.IsIndexType(d.MediaType) {
				return nil, fmt.Errorf("blob %s of manifest %s has the media type %s", d.Digest, desc.Digest, d.MediaType)
			}
		}
		return descs, nil
	})
	if err := images.Walk(ctx, handler, root); err != nil {
		return ocispec.Descriptor{}, err
	}

	// The walk visits a parent before its children, so its reverse puts each blob before its parents.
	slices.Reverse(walked)
	seen := make(map[digest.Digest]bool, len(walked))
	var blobs, manifests []ocispec.Descriptor
	for _, desc := range walked {
		if seen[desc.Digest] {
			continue
		}
		seen[desc.Digest] = true
		if images.IsManifestType(desc.MediaType) || images.IsIndexType(desc.MediaType) {
			manifests = append(manifests, desc)
		} else {
			blobs = append(blobs, desc)
		}
	}

	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(maxBlobCopies)
	for _, blob := range blobs {
		eg.Go(func() error {
			if _, err := packageCopy(egCtx, store, source, provider, blob); err != nil {
				return fmt.Errorf("failed to copy blob %s: %w", blob.Digest, err)
			}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return ocispec.Descriptor{}, err
	}
	// The root comes last, as a pusher leaves the tag at the last manifest.
	written := map[digest.Digest]ocispec.Descriptor{} // the new indexes, by the digest of the index they replace
	for _, mfst := range manifests {
		if images.IsIndexType(mfst.MediaType) {
			desc, err := copyIndex(ctx, provider, store, source, mfst, kept[mfst.Digest], written)
			if err != nil {
				return ocispec.Descriptor{}, fmt.Errorf("failed to copy index %s: %w", mfst.Digest, err)
			}
			if desc.Digest != mfst.Digest {
				written[mfst.Digest] = desc
			}
			continue
		}
		if _, err := packageCopy(ctx, store, source, provider, mfst); err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to copy manifest %s: %w", mfst.Digest, err)
		}
	}
	if desc, ok := written[root.Digest]; ok {
		return desc, nil
	}
	return root, nil
}

// copyIndex copies the index desc whose entries the copy keeps, or writes a new index over them if they are not all its own.
func copyIndex(ctx context.Context, provider content.Provider, store content.Ingester, source reference.Reference, desc ocispec.Descriptor, kept []ocispec.Descriptor, written map[digest.Digest]ocispec.Descriptor) (ocispec.Descriptor, error) {
	data, err := readVerifiedBlob(ctx, provider, desc)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	var idx ocispec.Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return ocispec.Descriptor{}, err
	}

	same := len(kept) == len(idx.Manifests)
	entries := slices.Clone(kept)
	for i, entry := range entries {
		if w, ok := written[entry.Digest]; ok {
			entries[i].MediaType, entries[i].Digest, entries[i].Size = w.MediaType, w.Digest, w.Size
			same = false
		}
	}
	if same {
		return packageCopy(ctx, store, source, provider, desc)
	}
	idx.Manifests = entries
	return packageJSON(ctx, store, desc.MediaType, idx)
}
