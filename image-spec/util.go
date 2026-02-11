package imagespec

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// manifest is a forked version of images.Manifest that returns all matching
// manifests instead of just the first one.
func manifest(ctx context.Context, provider content.Provider, image ocispec.Descriptor, platform platforms.MatchComparer) ([]ocispec.Manifest, error) {
	var (
		m        []ocispec.Manifest
		wasIndex bool
	)

	if err := images.Walk(ctx, images.HandlerFunc(func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		if images.IsManifestType(desc.MediaType) {
			p, err := content.ReadBlob(ctx, provider, desc)
			if err != nil {
				return nil, err
			}

			var manifest ocispec.Manifest
			if err := json.Unmarshal(p, &manifest); err != nil {
				return nil, err
			}

			if desc.Digest != image.Digest && platform != nil {
				if desc.Platform != nil && !platform.Match(*desc.Platform) {
					return nil, nil
				}

				if desc.Platform == nil {
					imagePlatform, err := images.ConfigPlatform(ctx, provider, manifest.Config)
					if err != nil {
						return nil, err
					}
					if !platform.Match(imagePlatform) {
						return nil, nil
					}

				}
			}

			m = append(m, manifest)

			return nil, nil
		} else if images.IsIndexType(desc.MediaType) {
			p, err := content.ReadBlob(ctx, provider, desc)
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
