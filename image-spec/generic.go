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
	"os"
	"slices"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"unikraft.com/x/image-spec/rootfs"
	"unikraft.com/x/mergefs"
)

// RuntimeKind is what runs an image.
type RuntimeKind int

const (
	// RuntimeKindUnikraft runs the kernel, initrd and ROMs of an image.
	RuntimeKindUnikraft RuntimeKind = iota
	// RuntimeKindDocker runs the tar layers of a linux image as its rootfs.
	RuntimeKindDocker
)

func (k RuntimeKind) String() string {
	switch k {
	case RuntimeKindUnikraft:
		return "unikraft"
	case RuntimeKindDocker:
		return "docker"
	}
	return fmt.Sprintf("RuntimeKind(%d)", int(k))
}

// IsGeneric reports if a manifest, whose config has the platform, is a Docker image: a linux image with only tar layers, none of them a Unikraft part.
func IsGeneric(mfst ocispec.Manifest, platform ocispec.Platform) bool {
	config := mfst.Config.MediaType
	if config != ocispec.MediaTypeImageConfig && config != images.MediaTypeDockerSchema2Config {
		return false
	}
	if platform.OS != "linux" || len(mfst.Layers) == 0 {
		return false
	}
	for _, layer := range mfst.Layers {
		if !rootfs.IsLayer(layer.MediaType) || isPart(layer) {
			return false
		}
	}
	return true
}

// isPart reports if a layer holds a Unikraft part: a kernel, its debug symbols or an initrd.
func isPart(layer ocispec.Descriptor) bool {
	for _, a := range []string{AnnotationKernelPath, AnnotationKernelDbgPath, AnnotationKernelInitrdPath} {
		if layer.Annotations[a] != "" {
			return true
		}
	}
	return false
}

// RootfsWriter writes the rootfs of the layers of a Docker image to dst, and gives opts to the merge of the layers.
type RootfsWriter func(ctx context.Context, dst *os.File, layers []mergefs.Layer, opts ...mergefs.Option) error

// WithRootfs makes a load write the rootfs of a Docker image with write (EROFS if nil), and its temporary files to dir (os.TempDir if empty).
func WithRootfs(dir string, write RootfsWriter) LoadOpt {
	return func(o *loadOptions) {
		o.rootfsDir = dir
		o.rootfsWrite = write
	}
}

// rootfsFile is the Initrd of a Docker image: the first Open writes the rootfs of the layers to a temporary file.
type rootfsFile struct {
	store  content.Provider
	layers []ocispec.Descriptor
	opts   loadOptions

	mu  sync.Mutex
	img *os.File
}

func newRootfsFile(store content.Provider, layers []ocispec.Descriptor, opts loadOptions) File {
	return newFileShared(&rootfsFile{store: store, layers: layers, opts: opts})
}

// isRootfs reports if f is the Initrd that a load makes from the layers of a Docker image.
func isRootfs(f File) bool {
	s, ok := f.(*fileShared)
	if !ok {
		return false
	}
	_, ok = s.inner.(*rootfsFile)
	return ok
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
	layers := make([]mergefs.Layer, len(f.layers))
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
	img, err := os.CreateTemp(f.opts.rootfsDir, "rootfs-*")
	if err != nil {
		return nil, err
	}
	spool := mergefs.WithSpoolDir(f.opts.rootfsDir)
	if f.opts.rootfsWrite != nil {
		err = f.opts.rootfsWrite(ctx, img, layers, spool)
	} else {
		err = mergefs.EROFS(ctx, img, layers, spool)
	}
	if err != nil {
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
	return newRootfsFile(f.store, f.layers, f.opts), nil
}

// Source returns no descriptor, as no blob holds the rootfs.
func (f *rootfsFile) Source() (ocispec.Descriptor, content.Provider) {
	return ocispec.Descriptor{}, nil
}

// GenericPlatforms returns a matcher of the linux platforms (base variant) whose Docker images run on the Unikraft platforms that platform matches.
func GenericPlatforms(platform platforms.MatchComparer) platforms.MatchComparer {
	return genericPlatforms{platform}
}

// genericPlatforms matches linux/amd64 and linux/arm64 when the Unikraft platforms of the architecture match.
type genericPlatforms struct {
	platforms.MatchComparer
}

func (g genericPlatforms) Match(p ocispec.Platform) bool {
	if p.OS != "linux" {
		return false
	}
	// Only the base variant, which Normalize makes empty, runs on all the CPUs of the architecture.
	p = platforms.Normalize(p)
	arch, err := NormalizeArch(p.Architecture)
	if err != nil || p.Variant != "" {
		return false
	}
	for _, plat := range []string{PlatformKraftcloud, PlatformFirecracker, PlatformQemu} {
		if g.MatchComparer.Match(ocispec.Platform{OS: plat, Architecture: arch}) {
			return true
		}
	}
	return false
}

// loadFallback loads the Docker images of desc for a Unikraft platform, after the load found no manifest for the platform (err).
func loadFallback(ctx context.Context, store content.Provider, desc ocispec.Descriptor, platform platforms.MatchComparer, err error, opts loadOptions) ([]*Image, error) {
	if platform == nil {
		return nil, err
	}
	imgs, gerr := loadAll(ctx, store, desc, genericPlatforms{platform}, platform, opts)
	if isNoMatch(gerr) {
		return nil, err
	}
	if gerr != nil {
		return nil, fmt.Errorf("failed to load the Docker image: %w", gerr)
	}
	imgs = slices.DeleteFunc(imgs, func(img *Image) bool {
		if img.Runtime == RuntimeKindDocker {
			return false
		}
		_ = img.Close()
		return true
	})
	if len(imgs) == 0 {
		return nil, err
	}
	return imgs, nil
}
