// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/kraftfile"

	"unikraft.com/x/build/buildflags"
)

type BuildOpts struct {
	Rootfs FSOpts
	Roms   []FSOpts

	Runtime string

	// Kernel is a host path to an already-built kernel binary, used in place
	// of resolving one from Runtime.
	Kernel string

	// Platform lists the targets to build. When empty, PlatformMatcher
	// selects them from the runtime's targets.
	Platform        []ocispec.Platform
	PlatformMatcher platforms.MatchComparer

	Cmd    []string
	Env    kraftfile.Map
	Labels map[string]string

	// Buildkit params
	BuildArg []string
	Target   string
	Secrets  []*buildflags.Secret
	SSH      []*buildflags.SSH

	NoCache bool
}

type FSOpts struct {
	// Source holds Path. When it is nil, Path is on the host.
	Source     Source
	Path       string
	Type       kraftfile.SourceType
	Dockerfile string

	// Output params
	Format   kraftfile.FsType
	Pad      int64 // pad file to specified size
	Compress bool
}

// FilterArch keeps only the targets of arches. Each arch can be an alias, such
// as amd64 for x86_64.
func (o *BuildOpts) FilterArch(arches ...string) error {
	if len(arches) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(arches))
	for _, arch := range arches {
		norm, err := imagespec.NormalizeArch(arch)
		if err != nil {
			return err
		}
		normalized = append(normalized, norm)
	}
	arches = normalized

	if len(o.Platform) == 0 {
		if o.Runtime != "" {
			o.PlatformMatcher = onlyArch(arches...)
			return nil
		}
		for _, arch := range arches {
			if slices.ContainsFunc(o.Platform, func(p ocispec.Platform) bool {
				return p.Architecture == arch
			}) {
				continue
			}
			o.Platform = append(o.Platform, ocispec.Platform{
				Architecture: arch, OS: imagespec.PlatformKraftcloud,
			})
		}
		return nil
	}

	available := make([]string, 0, len(o.Platform))
	for _, p := range o.Platform {
		available = append(available, platforms.Format(p))
	}
	o.Platform = slices.DeleteFunc(o.Platform, func(p ocispec.Platform) bool {
		norm, err := imagespec.NormalizeArch(p.Architecture)
		return err != nil || !slices.Contains(arches, norm)
	})
	if len(o.Platform) == 0 {
		return fmt.Errorf("no target with architecture %s: targets are %s",
			strings.Join(arches, ", "), strings.Join(available, ", "))
	}
	return nil
}

// erofsFeatureFlags are the OSFeatures that signal EROFS support in a runtime.
var erofsFeatureFlags = []string{
	"CONFIG_LIBUKFS_EROFS=y",
	"CONFIG_EROFS_FS=y",
}

// runtimeHasErofsSupport reports whether the given runtime's OSFeatures
// indicate EROFS support.
func runtimeHasErofsSupport(p ocispec.Platform) bool {
	for _, f := range p.OSFeatures {
		for _, e := range erofsFeatureFlags {
			if strings.EqualFold(f, e) {
				return true
			}
		}
	}
	return false
}

// hasKernel reports whether the build produces a kernel.
func (o BuildOpts) hasKernel() bool {
	return o.Runtime != "" || o.Kernel != ""
}

// defaultRootfsFormat returns the default rootfs format for opts. Kernel-less
// builds default to erofs. Builds with a kernel default to erofs only when
// every kernel platform advertises EROFS support, and to cpio otherwise.
func defaultRootfsFormat(opts BuildOpts) kraftfile.FsType {
	if !opts.hasKernel() {
		return kraftfile.FsTypeErofs
	}
	for _, p := range opts.Platform {
		if !runtimeHasErofsSupport(p) {
			return kraftfile.FsTypeCpio
		}
	}
	return kraftfile.FsTypeErofs
}

// Build a unikraft image based on the provided build options.
func (b Builder) Build(ctx context.Context, opts BuildOpts) ([]*imagespec.Image, error) {
	if opts.Runtime == "" && opts.Kernel == "" && opts.Rootfs.Path == "" && len(opts.Roms) == 0 {
		return nil, fmt.Errorf("no runtime, kernel, rootfs, or roms specified: nothing to build")
	}

	meta := imagespec.ImageMetadata{
		Created: new(time.Now()),
	}

	// Build kernel if a runtime is specified. This also determines the
	// set of platforms we're building for. An already-built kernel instead
	// takes its platform from the declared targets.
	var kernels []*imagespec.Image
	switch {
	case opts.Kernel != "":
		var err error
		kernels, err = loadKernel(opts)
		if err != nil {
			return nil, err
		}
		defer func() {
			for _, kernel := range kernels {
				kernel.Close()
			}
		}()
	case opts.Runtime != "":
		var err error
		kernels, err = b.buildKernel(ctx, opts)
		if err != nil {
			return nil, err
		}
		defer func() {
			for _, kernel := range kernels {
				kernel.Close()
			}
		}()
		opts.Platform = make([]ocispec.Platform, 0, len(kernels))
		for _, kernel := range kernels {
			opts.Platform = append(opts.Platform, kernel.Image.Platform)
		}
	}

	if len(opts.Platform) == 0 {
		return nil, fmt.Errorf("no platform targets specified")
	}

	// Build ROMs if any are specified.
	romFiles, err := b.buildRoms(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, perPlat := range romFiles {
			for _, f := range perPlat {
				if f != nil {
					_ = f.Cleanup()
				}
			}
		}
	}()

	// Build rootfs if a path is specified.
	var roots []*imagespec.Image
	if opts.Rootfs.Path != "" {
		var err error
		roots, err = b.BuildRootfs(ctx, opts)
		if err != nil {
			return nil, err
		}
		defer func() {
			for _, root := range roots {
				root.Close()
			}
		}()
	}

	// When we have both kernels and rootfs, they must match 1:1 by platform.
	if len(kernels) > 0 && len(roots) > 0 && len(kernels) != len(roots) {
		return nil, fmt.Errorf("internal error: number of kernels (%d) does not match number of root filesystems (%d)",
			len(kernels), len(roots))
	}

	// Check each part against its platform before the images own the parts,
	// so that the deferred cleanup releases all of them on an error.
	for i, p := range opts.Platform {
		pID := platforms.Format(p)
		if i < len(kernels) {
			if got := platforms.Format(kernels[i].Image.Platform); got != pID {
				return nil, fmt.Errorf("internal error: kernel platform %s does not match expected platform %s", got, pID)
			}
		}
		if i < len(roots) {
			if got := platforms.Format(roots[i].Image.Platform); got != pID {
				return nil, fmt.Errorf("internal error: rootfs platform %s does not match expected platform %s", got, pID)
			}
		}
		for j := range romFiles {
			if i >= len(romFiles[j]) || romFiles[j][i] == nil {
				return nil, fmt.Errorf("internal error: no rom %d built for platform %s", j, pID)
			}
		}
	}

	// Assemble images, one per platform.
	images := make([]*imagespec.Image, 0, len(opts.Platform))
	for i, p := range opts.Platform {
		imgOpts := []imagespec.NewImageOpt{
			imagespec.WithImageMetadata(meta),
			imagespec.WithPlatform(p),
		}

		// Attach kernel if available.
		if i < len(kernels) {
			imgOpts = append(imgOpts, imagespec.WithKernel(kernels[i].Kernel))
			kernels[i].Kernel = nil
		}

		// Attach rootfs/initrd and use its config if available.
		cfg := applyConfigOverrides(ocispec.ImageConfig{}, opts)
		if i < len(roots) {
			imgOpts = append(imgOpts, imagespec.WithInitrd(roots[i].Initrd))
			roots[i].Initrd = nil
			// The rootfs build may have produced a richer config (e.g. from
			// a Dockerfile or an OCI image). Use it as the base and layer our
			// overrides on top.
			cfg = applyConfigOverrides(roots[i].Image.Config, opts)
		}
		imgOpts = append(imgOpts, imagespec.WithImageConfig(cfg))

		// Attach roms if available. A ROM payload is a compiled artifact, so
		// each image gets the ROM that was built for its own platform.
		for j := range romFiles {
			imgOpts = append(imgOpts, imagespec.WithRom(romFiles[j][i]))
			romFiles[j][i] = nil // ownership moves to the image
		}

		images = append(images, imagespec.NewImage(imgOpts...))
	}

	return images, nil
}
