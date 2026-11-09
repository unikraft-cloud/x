// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"unikraft.com/x/image-spec/rootfs"
)

// annotationPrefix is the prefix of the annotations of Unikraft images.
const annotationPrefix = "org.unikraft."

// isGeneric reports if a manifest has no Unikraft annotations and only tar layers.
func isGeneric(mfst ocispec.Manifest) bool {
	if hasUnikraftAnnotation(mfst.Annotations) {
		return false
	}
	for _, layer := range mfst.Layers {
		if hasUnikraftAnnotation(layer.Annotations) || !rootfs.IsLayer(layer.MediaType) {
			return false
		}
	}
	return true
}

func hasUnikraftAnnotation(annotations map[string]string) bool {
	for k := range annotations {
		if strings.HasPrefix(k, annotationPrefix) {
			return true
		}
	}
	return false
}

// rootfsFile is the Initrd of a generic image: the first Open writes the layers to a temporary EROFS image.
type rootfsFile struct {
	store  content.Provider
	layers []ocispec.Descriptor

	mu  sync.Mutex
	img *os.File
}

func newRootfsFile(store content.Provider, layers []ocispec.Descriptor) File {
	return newFileShared(&rootfsFile{store: store, layers: layers})
}

func (f *rootfsFile) Path() string { return "" }

func (f *rootfsFile) Open(ctx context.Context) (io.ReadCloser, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.img == nil {
		img, err := f.convert(ctx)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to convert the layers to a rootfs: %w", err)
		}
		f.img = img
	}
	fi, err := f.img.Stat()
	if err != nil {
		return nil, 0, err
	}
	rc, err := os.Open(f.img.Name())
	if err != nil {
		return nil, 0, err
	}
	return rc, fi.Size(), nil
}

func (f *rootfsFile) convert(ctx context.Context) (*os.File, error) {
	layers := make([]rootfs.Layer, len(f.layers))
	for i, desc := range f.layers {
		blob := NewContentStoreFile(f.store, desc, "")
		layer, err := rootfs.NewLayer(desc.MediaType, func() (io.ReadCloser, error) {
			rc, _, err := blob.Open(ctx)
			return rc, err
		})
		if err != nil {
			return nil, err
		}
		layers[i] = layer
	}
	img, err := os.CreateTemp("", "rootfs-*.erofs")
	if err != nil {
		return nil, err
	}
	if err := rootfs.EROFS(ctx, img, layers); err != nil {
		return nil, errors.Join(err, img.Close(), os.Remove(img.Name()))
	}
	return img, nil
}

func (f *rootfsFile) Cleanup() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.img == nil {
		return nil
	}
	err := errors.Join(f.img.Close(), os.Remove(f.img.Name()))
	f.img = nil
	return err
}

func (f *rootfsFile) Clone() (File, error) {
	return newRootfsFile(f.store, f.layers), nil
}

// Source returns no descriptor, as no blob holds the EROFS image.
func (f *rootfsFile) Source() (ocispec.Descriptor, content.Provider) {
	return ocispec.Descriptor{}, nil
}

// genericPlatforms matches linux/amd64 and linux/arm64 when the Unikraft platforms of the architecture match.
type genericPlatforms struct {
	platforms.MatchComparer
}

func (g genericPlatforms) Match(p ocispec.Platform) bool {
	if p.OS != "linux" {
		return false
	}
	arch, err := NormalizeArch(p.Architecture)
	if err != nil {
		return false
	}
	// Only the base variant, which all the CPUs of the architecture run.
	base := "v1"
	if arch == ArchitectureArm {
		base = "v8"
	}
	if p.Variant != "" && p.Variant != base {
		return false
	}
	for _, plat := range []string{PlatformKraftcloud, PlatformFirecracker, PlatformQemu} {
		if g.MatchComparer.Match(ocispec.Platform{OS: plat, Architecture: arch}) {
			return true
		}
	}
	return false
}

// loadGeneric loads the generic images of desc for a Unikraft platform, or returns errdefs.ErrNotFound.
func loadGeneric(ctx context.Context, store content.Provider, desc ocispec.Descriptor, platform platforms.MatchComparer) ([]*Image, error) {
	mfsts, err := ManifestDescriptors(ctx, store, desc, ignoringOSFeatures(genericPlatforms{platform}))
	if err != nil {
		return nil, err
	}
	annotations, err := indexAnnotations(ctx, store, desc)
	if err != nil {
		return nil, err
	}
	var imgs []*Image
	for _, mfst := range mfsts {
		img, err := loadCtrdImageMfst(ctx, store, desc, mfst, platform)
		if err != nil {
			for _, img := range imgs {
				err = errors.Join(err, img.Close())
			}
			return nil, err
		}
		if !img.Generic {
			_ = img.Close()
			continue
		}
		img.IndexAnnotations = maps.Clone(annotations)
		imgs = append(imgs, img)
	}
	if len(imgs) == 0 {
		return nil, fmt.Errorf("no generic image for the platform in %v: %w", desc.Digest, errdefs.ErrNotFound)
	}
	return imgs, nil
}

// forUnikraft reports if platform asks for a Unikraft platform that matches the linux platform p only with genericPlatforms.
func forUnikraft(platform platforms.MatchComparer, p ocispec.Platform) bool {
	return platform != nil && !platform.Match(p) && genericPlatforms{platform}.Match(p)
}

// loadFallback loads the generic images of desc for a Unikraft platform, after ManifestDescriptors failed with err.
func loadFallback(ctx context.Context, store content.Provider, desc ocispec.Descriptor, platform platforms.MatchComparer, err error) ([]*Image, error) {
	if platform == nil || !errors.Is(err, errdefs.ErrNotFound) {
		return nil, fmt.Errorf("failed to get image manifest: %w", err)
	}
	imgs, gerr := loadGeneric(ctx, store, desc, platform)
	if errors.Is(gerr, errdefs.ErrNotFound) {
		return nil, fmt.Errorf("failed to get image manifest: %w", err)
	}
	if gerr != nil {
		return nil, fmt.Errorf("failed to load the generic image: %w", gerr)
	}
	return imgs, nil
}
