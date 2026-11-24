// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
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

// maxCacheSize bounds the bytes that the blob cache of one copy keeps.
const maxCacheSize int64 = 64 << 20

// Copy copies the image of src to dest blob by blob, so every digest stays, and pins dest to the root; platform (nil for all) selects the manifests of an index as Load does.
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

// copyContent copies root and its tree from provider, each blob before its parents, as Copy does, the root to tagged and the rest to store, and returns the root it wrote.
func copyContent(ctx context.Context, provider content.Provider, store, tagged content.Ingester, source reference.Reference, root ocispec.Descriptor, platform platforms.MatchComparer) (ocispec.Descriptor, error) {
	if !images.IsManifestType(root.MediaType) && !images.IsIndexType(root.MediaType) {
		return ocispec.Descriptor{}, fmt.Errorf("root %s has the media type %q, which is not that of a manifest or an index", root.Digest, root.MediaType)
	}
	provider = newBlobCache(provider, maxCacheSize)
	sel := &selection{provider: provider, platform: ignoringOSFeatures(platform), kept: map[digest.Digest][]ocispec.Descriptor{}}
	var walked []ocispec.Descriptor
	handler := images.HandlerFunc(func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		// A pusher puts the digest in its requests before it reads the blob.
		if err := desc.Digest.Validate(); err != nil {
			return nil, fmt.Errorf("blob %q: %w", desc.Digest, err)
		}
		// A registry serves no foreign layer.
		if images.IsNonDistributable(desc.MediaType) {
			return nil, images.ErrSkipDesc
		}
		walked = append(walked, desc)
		if images.IsIndexType(desc.MediaType) {
			entries, err := sel.entries(ctx, desc)
			if err != nil {
				return nil, err
			}
			// A parent drops an index that keeps nothing, so only the root can get here with nothing.
			if sel.platform != nil && len(entries) == 0 {
				return nil, fmt.Errorf("no manifest of index %s matches the platform: %w", desc.Digest, errdefs.ErrNotFound)
			}
			return entries, nil
		}
		if !images.IsManifestType(desc.MediaType) {
			return nil, nil
		}
		descs, err := verifiedChildren(ctx, provider, desc)
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
	written := map[digest.Digest]ocispec.Descriptor{} // the new indexes, by the digest of the index they replace
	for _, mfst := range manifests {
		dest := store
		if mfst.Digest == root.Digest {
			dest = tagged
		}
		if images.IsIndexType(mfst.MediaType) {
			desc, err := copyIndex(ctx, provider, dest, source, mfst, sel.kept[mfst.Digest], written)
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

// blobCache is a provider that keeps the blobs that readVerifiedBlob reads from it, up to a total
// size, so that a copy fetches each manifest, index and config once.
type blobCache struct {
	content.Provider
	mu    sync.Mutex
	blobs map[digest.Digest][]byte
	room  int64 // the bytes that the cache can still keep
}

// newBlobCache returns a cache of provider that keeps at most size bytes.
func newBlobCache(provider content.Provider, size int64) *blobCache {
	return &blobCache{Provider: provider, blobs: map[digest.Digest][]byte{}, room: size}
}

// ReaderAt reads from memory a blob that the cache keeps.
func (c *blobCache) ReaderAt(ctx context.Context, desc ocispec.Descriptor) (content.ReaderAt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	data, ok := c.blobs[desc.Digest]
	c.mu.Unlock()
	if ok {
		return inlineReaderAt{bytes.NewReader(data)}, nil
	}
	return c.Provider.ReaderAt(ctx, desc)
}

// keep keeps data, unless the cache has it or has no room for it.
func (c *blobCache) keep(dgst digest.Digest, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.blobs[dgst]; ok || int64(len(data)) > c.room {
		return
	}
	c.blobs[dgst] = data
	c.room -= int64(len(data))
}

// verifiedChildren returns the children of desc from its verified data, as images.Children reads with no size limit and no digest check.
func verifiedChildren(ctx context.Context, provider content.Provider, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
	data, err := readVerifiedBlob(ctx, provider, desc, docker.MaxManifestSize)
	if err != nil {
		return nil, err
	}
	desc.Data = data
	return images.Children(ctx, provider, desc)
}

// selection selects the entries of the indexes of a copy as Load does, and keeps the entries of each index it reads.
type selection struct {
	provider content.Provider
	platform platforms.MatchComparer // nil for all
	kept     map[digest.Digest][]ocispec.Descriptor
}

// entries returns the entries of the index desc that the platform selects.
func (s *selection) entries(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
	if entries, ok := s.kept[desc.Digest]; ok {
		return entries, nil
	}
	entries, err := verifiedChildren(ctx, s.provider, desc)
	if err != nil {
		return nil, err
	}
	if s.platform != nil {
		var keep []ocispec.Descriptor
		for _, entry := range entries {
			ok, err := s.selects(ctx, entry)
			if err != nil {
				return nil, err
			}
			if ok {
				keep = append(keep, entry)
			}
		}
		entries = keep
	}
	s.kept[desc.Digest] = entries
	return entries, nil
}

// selects reports whether the platform selects the index entry desc: an index if it keeps an entry, and a manifest without a platform by its config.
func (s *selection) selects(ctx context.Context, desc ocispec.Descriptor) (bool, error) {
	switch {
	case desc.Platform != nil && !s.platform.Match(*desc.Platform):
		return false, nil
	case images.IsIndexType(desc.MediaType):
		entries, err := s.entries(ctx, desc)
		return len(entries) > 0, err
	case images.IsManifestType(desc.MediaType):
		return manifestMatches(ctx, s.provider, desc, s.platform)
	default:
		return true, nil
	}
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
