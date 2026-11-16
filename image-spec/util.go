// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Manifests is a forked version of images.Manifest that returns all matching
// manifests instead of just the first one.
//
// The manifests are ordered by how well they match the given platform, best
// first.
func Manifests(ctx context.Context, provider content.Provider, image ocispec.Descriptor, platform platforms.MatchComparer) ([]ocispec.Manifest, error) {
	descs, err := ManifestDescriptors(ctx, provider, image, platform)
	if err != nil {
		return nil, err
	}
	m := make([]ocispec.Manifest, 0, len(descs))
	for _, desc := range descs {
		manifest, err := readManifest(ctx, provider, desc)
		if err != nil {
			return nil, err
		}
		m = append(m, manifest)
	}
	return m, nil
}

func readManifest(ctx context.Context, provider content.Provider, desc ocispec.Descriptor) (ocispec.Manifest, error) {
	p, err := readVerifiedBlob(ctx, provider, desc)
	if err != nil {
		return ocispec.Manifest{}, err
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(p, &manifest); err != nil {
		return ocispec.Manifest{}, err
	}
	return manifest, nil
}

// loadedIndex is an image index as it was loaded.
type loadedIndex struct {
	desc  ocispec.Descriptor
	raw   []byte
	index ocispec.Index
	// inner is the only entry of the index if it is an index, as an archive's index.json can list.
	inner *loadedIndex
	// loaded is the manifests that the load returned, in its order.
	loaded []ocispec.Descriptor
}

// readIndex reads the image index at desc, or returns nil for a manifest.
func readIndex(ctx context.Context, provider content.Provider, desc ocispec.Descriptor) (*loadedIndex, error) {
	if !images.IsIndexType(desc.MediaType) {
		return nil, nil
	}
	raw, err := readVerifiedBlob(ctx, provider, desc)
	if err != nil {
		return nil, fmt.Errorf("failed to read image index: %w", err)
	}
	idx := &loadedIndex{desc: ocispec.Descriptor{MediaType: desc.MediaType, Digest: desc.Digest, Size: desc.Size}, raw: raw}
	if err := json.Unmarshal(raw, &idx.index); err != nil {
		return nil, fmt.Errorf("failed to unmarshal image index: %w", err)
	}
	if len(idx.index.Manifests) == 1 {
		if idx.inner, err = readIndex(ctx, provider, idx.index.Manifests[0]); err != nil {
			return nil, err
		}
	}
	return idx, nil
}

// annotations returns a copy of the annotations of the innermost index.
func (l *loadedIndex) annotations() map[string]string {
	for l.inner != nil {
		l = l.inner
	}
	return maps.Clone(l.index.Annotations)
}

// sourceIndex returns the index that imgs were loaded from if they are all its manifests, in the order that the load returned them, so a save copies it.
func sourceIndex(imgs []*Image, mfsts []ocispec.Descriptor, annotations map[string]string) *loadedIndex {
	if len(imgs) == 0 || imgs[0].index == nil {
		return nil
	}
	idx := imgs[0].index
	for _, img := range imgs {
		if !img.byManifest() || img.manifest == nil || img.index == nil || img.index.desc.Digest != idx.desc.Digest {
			return nil
		}
	}
	if !maps.Equal(idx.index.Annotations, annotations) {
		return nil
	}
	want, err := json.Marshal(idx.loaded)
	if err != nil {
		return nil
	}
	got, err := json.Marshal(mfsts)
	if err != nil || !bytes.Equal(got, want) {
		return nil
	}
	// The load must have returned every manifest of the index.
	src, err := descriptorKeys(idx.index.Manifests)
	if err != nil {
		return nil
	}
	dst, err := descriptorKeys(idx.loaded)
	if err != nil || !slices.Equal(src, dst) {
		return nil
	}
	return idx
}

// descriptorKeys returns descs as sorted JSON, to compare them in any order.
func descriptorKeys(descs []ocispec.Descriptor) ([]string, error) {
	keys := make([]string, len(descs))
	for i, desc := range descs {
		key, err := json.Marshal(desc)
		if err != nil {
			return nil, err
		}
		keys[i] = string(key)
	}
	slices.Sort(keys)
	return keys, nil
}

// copyableBlobs returns the config and the layers of mfst that a registry serves, each once.
// A config or layer of a manifest or index type is an error: a pusher puts those to the tag.
func copyableBlobs(mfst ocispec.Manifest) ([]ocispec.Descriptor, error) {
	for _, desc := range append([]ocispec.Descriptor{mfst.Config}, mfst.Layers...) {
		if images.IsManifestType(desc.MediaType) || images.IsIndexType(desc.MediaType) {
			return nil, fmt.Errorf("blob %s of the manifest has the media type %s", desc.Digest, desc.MediaType)
		}
	}
	blobs := []ocispec.Descriptor{mfst.Config}
	seen := map[digest.Digest]bool{mfst.Config.Digest: true}
	for _, layer := range mfst.Layers {
		if !images.IsNonDistributable(layer.MediaType) && !seen[layer.Digest] {
			seen[layer.Digest] = true
			blobs = append(blobs, layer)
		}
	}
	return blobs, nil
}

// ConfigPlatform is a forked version of images.ConfigPlatform that checks the
// config against its digest.
func ConfigPlatform(ctx context.Context, provider content.Provider, configDesc ocispec.Descriptor) (ocispec.Platform, error) {
	p, err := readVerifiedBlob(ctx, provider, configDesc)
	if err != nil {
		return ocispec.Platform{}, err
	}

	var imagePlatform ocispec.Platform
	if err := json.Unmarshal(p, &imagePlatform); err != nil {
		return ocispec.Platform{}, err
	}

	return platforms.Normalize(imagePlatform), nil
}

// ManifestDescriptors is like Manifests, but returns the descriptors of the
// matching manifests instead of the manifests themselves.
func ManifestDescriptors(ctx context.Context, provider content.Provider, image ocispec.Descriptor, platform platforms.MatchComparer) ([]ocispec.Descriptor, error) {
	var (
		m        []ocispec.Descriptor
		wasIndex bool
	)

	if err := images.Walk(ctx, images.HandlerFunc(func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		if images.IsManifestType(desc.MediaType) {
			if desc.Digest != image.Digest && platform != nil {
				if desc.Platform != nil && !platform.Match(*desc.Platform) {
					return nil, nil
				}

				if desc.Platform == nil {
					manifest, err := readManifest(ctx, provider, desc)
					if err != nil {
						return nil, err
					}
					imagePlatform, err := ConfigPlatform(ctx, provider, manifest.Config)
					if err != nil {
						return nil, err
					}
					if !platform.Match(imagePlatform) {
						return nil, nil
					}
				}
			}

			m = append(m, desc)

			return nil, nil
		} else if images.IsIndexType(desc.MediaType) {
			p, err := readVerifiedBlob(ctx, provider, desc)
			if err != nil {
				return nil, err
			}

			var idx ocispec.Index
			if err := json.Unmarshal(p, &idx); err != nil {
				return nil, err
			}

			if platform == nil {
				return idx.Manifests, nil
			}

			var descs []ocispec.Descriptor
			for _, d := range idx.Manifests {
				if d.Platform == nil || platform.Match(*d.Platform) {
					descs = append(descs, d)
				}
			}

			sort.SliceStable(descs, func(i, j int) bool {
				if descs[i].Platform == nil {
					return false
				}
				if descs[j].Platform == nil {
					return true
				}
				return platform.Less(*descs[i].Platform, *descs[j].Platform)
			})

			wasIndex = true

			return descs, nil
		}
		return nil, fmt.Errorf("unexpected media type %v for %v: %w", desc.MediaType, desc.Digest, errdefs.ErrNotFound)
	}), image); err != nil {
		return nil, err
	}

	if len(m) == 0 {
		err := fmt.Errorf("manifest %v: %w", image.Digest, errdefs.ErrNotFound)
		if wasIndex {
			err = fmt.Errorf("no match for platform in manifest %v: %w", image.Digest, errdefs.ErrNotFound)
		}
		return nil, err
	}

	return m, nil
}

// ignoringOSFeatures strips OSFeatures from candidates before matching, so a
// platform without them still matches descriptors that carry them.
func ignoringOSFeatures(m platforms.MatchComparer) platforms.MatchComparer {
	if m == nil {
		return nil
	}
	return ignoringOSFeaturesMatcher{m}
}

type ignoringOSFeaturesMatcher struct {
	platforms.MatchComparer
}

func (m ignoringOSFeaturesMatcher) Match(p ocispec.Platform) bool {
	p.OSFeatures = nil
	return m.MatchComparer.Match(p)
}

func (m ignoringOSFeaturesMatcher) Less(p1, p2 ocispec.Platform) bool {
	p1.OSFeatures = nil
	p2.OSFeatures = nil
	return m.MatchComparer.Less(p1, p2)
}
