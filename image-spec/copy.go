// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/remotes/docker"
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
		// A pusher of a reference with a digest puts each manifest by its digest.
		byDigest, err := accessor.remote.Pusher(ctx, named.Name()+"@"+root.Digest.String())
		if err != nil {
			return Resolved{}, fmt.Errorf("failed to get pusher for image %q: %w", named, err)
		}
		store := ingestRef(contentutil.FromPusher(byDigest), named.Name())
		tagged := ingestRef(contentutil.FromPusher(pusher), named.Name())
		root, err := copyContent(ctx, provider, store, tagged, src.source(), root, platform)
		if err != nil {
			return Resolved{}, fmt.Errorf("failed to copy image to %q: %w", named, err)
		}
		return savedRegistryImage(dest, named, root)
	case schemes.OCILayout:
		path, tag := splitPathTag(dest.Path)
		root, err := copyToOCILayout(ctx, path, cmp.Or(tag, "latest"), provider, src.source(), root, platform)
		if err != nil {
			return Resolved{}, err
		}
		return Resolved{Location: *dest, Descriptor: root}, nil
	case schemes.OCIArchive:
		path, tag, err := tarballSaveTag(ctx, dest.Path)
		if err != nil {
			return Resolved{}, err
		}
		root, err := replaceTarball(path, func(w io.Writer) (ocispec.Descriptor, error) {
			return copyToTarball(ctx, w, tag, provider, src.source(), root, platform)
		})
		if err != nil {
			return Resolved{}, err
		}
		return Resolved{Location: *dest, Descriptor: root}, nil
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
		u, err := tarballURL(res.Location)
		if err != nil {
			return nil, ocispec.Descriptor{}, nil, fmt.Errorf("parsing image reference %q: %w", reference.StripQuery(res.Location.String()), err)
		}
		body, err := openTarballRemote(ctx, u, accessor.httpClient, accessor.registryHeaders)
		if err != nil {
			return nil, ocispec.Descriptor{}, nil, err
		}
		defer body.Close()
		store, root, cleanup, err := importTarball(ctx, body, "", res.Descriptor)
		if err != nil {
			return nil, ocispec.Descriptor{}, nil, fmt.Errorf("tarball %q: %w", reference.StripQuery(u.String()), err)
		}
		return store, root, cleanup, nil
	case schemes.OCILayout:
		path, tag := splitPathTag(res.Location.Path)
		return openOCILayout(path, tag, res.Descriptor)
	case schemes.OCIArchive:
		path, tag := splitPathTag(res.Location.Path)
		f, err := os.Open(path)
		if err != nil {
			return nil, ocispec.Descriptor{}, nil, fmt.Errorf("failed to open tarball: %w", err)
		}
		defer f.Close()
		store, root, cleanup, err := importTarball(ctx, f, tag, res.Descriptor)
		if err != nil {
			return nil, ocispec.Descriptor{}, nil, fmt.Errorf("tarball %q: %w", path, err)
		}
		return store, root, cleanup, nil
	default:
		return nil, ocispec.Descriptor{}, nil, fmt.Errorf("unsupported location scheme: %q", res.Location.Scheme)
	}
}

// copyContent copies root and its tree from provider, each blob before its parents, as Copy does, the root to tagged and the rest to store, and returns the root it wrote.
func copyContent(ctx context.Context, provider content.Provider, store, tagged content.Ingester, source reference.Reference, root ocispec.Descriptor, platform platforms.MatchComparer) (ocispec.Descriptor, error) {
	if !images.IsManifestType(root.MediaType) && !images.IsIndexType(root.MediaType) {
		return ocispec.Descriptor{}, fmt.Errorf("root %s has the media type %q, which is not that of a manifest or an index", root.Digest, root.MediaType)
	}
	platform = ignoringOSFeatures(platform)
	var walked []ocispec.Descriptor
	kept := map[digest.Digest][]ocispec.Descriptor{} // the entries of each index that platform selects
	// children reads with no size limit and no digest check: give it the verified data.
	children := func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		data, err := readVerifiedBlob(ctx, provider, desc, docker.MaxManifestSize)
		if err != nil {
			return nil, err
		}
		desc.Data = data
		return images.Children(ctx, provider, desc)
	}
	// selected returns the entries of the index desc that platform selects as Load picks them, without an index that keeps none.
	var selected func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error)
	selected = func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		if descs, ok := kept[desc.Digest]; ok {
			return descs, nil
		}
		descs, err := children(ctx, desc)
		if err != nil || platform == nil {
			kept[desc.Digest] = descs
			return descs, err
		}
		var keep []ocispec.Descriptor
		for _, d := range descs {
			ok := d.Platform == nil || platform.Match(*d.Platform)
			switch {
			case ok && images.IsIndexType(d.MediaType):
				sub, err := selected(ctx, d)
				if err != nil {
					return nil, err
				}
				ok = len(sub) > 0
			case ok && images.IsManifestType(d.MediaType):
				if ok, err = manifestMatches(ctx, provider, d, platform); err != nil {
					return nil, err
				}
			}
			if ok {
				keep = append(keep, d)
			}
		}
		kept[desc.Digest] = keep
		return keep, nil
	}
	// A registry serves no foreign layer.
	handler := remotes.SkipNonDistributableBlobs(func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		// A pusher puts the digest in its requests before it reads the blob.
		if err := desc.Digest.Validate(); err != nil {
			return nil, fmt.Errorf("blob %q: %w", desc.Digest, err)
		}
		walked = append(walked, desc)
		switch {
		case images.IsIndexType(desc.MediaType):
			descs, err := selected(ctx, desc)
			if err != nil {
				return nil, err
			}
			// A parent drops an index that keeps nothing, so only the root gets here with nothing.
			if platform != nil && len(descs) == 0 {
				return nil, fmt.Errorf("no manifest of index %s matches the platform: %w", desc.Digest, errdefs.ErrNotFound)
			}
			return descs, nil
		case images.IsManifestType(desc.MediaType):
			descs, err := children(ctx, desc)
			if err != nil {
				return nil, err
			}
			// A pusher puts a blob of a manifest type as a manifest, so a blob must not have one.
			for _, d := range descs {
				if images.IsManifestType(d.MediaType) || images.IsIndexType(d.MediaType) {
					return nil, fmt.Errorf("blob %s of manifest %s has the media type %s", d.Digest, desc.Digest, d.MediaType)
				}
			}
			return descs, nil
		}
		return nil, nil
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
	// The root comes last, so that the tag names it only when all its tree is written.
	// TODO: the walk already read and verified these manifests and indexes, and could hand them over instead of fetching them again.
	written := map[digest.Digest]ocispec.Descriptor{} // the new indexes, by the digest of the index they replace
	for _, mfst := range manifests {
		dest := store
		if mfst.Digest == root.Digest {
			dest = tagged
		}
		if images.IsIndexType(mfst.MediaType) {
			desc, err := copyIndex(ctx, provider, dest, source, mfst, kept[mfst.Digest], written)
			if err != nil {
				return ocispec.Descriptor{}, fmt.Errorf("failed to copy index %s: %w", mfst.Digest, err)
			}
			if desc.Digest != mfst.Digest {
				written[mfst.Digest] = desc
			}
			continue
		}
		if _, err := packageCopy(ctx, dest, source, provider, mfst); err != nil {
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
	data, err := readVerifiedBlob(ctx, provider, desc, docker.MaxManifestSize)
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
