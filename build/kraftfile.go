// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"unikraft.com/x/kraftfile"
)

// KraftfileBuildOpts returns the build options of kf. The source paths of kf
// resolve in src, and a LocalSource turns into host paths.
func KraftfileBuildOpts(kf *kraftfile.Kraftfile, src Source) (BuildOpts, error) {
	var opts BuildOpts

	opts.Cmd = []string(kf.Cmd)
	opts.Env = kf.Env
	opts.Labels = kf.Labels

	if kf.Runtime != nil {
		opts.Runtime = string(*kf.Runtime)
	}

	if kf.Kernel != nil {
		kernel, err := kraftfileKernel(kf, src)
		if err != nil {
			return BuildOpts{}, err
		}
		opts.Kernel = kernel
	}

	if kf.Libraries != nil {
		// these are the same build process as kf.Kernel
		return BuildOpts{}, fmt.Errorf("library configuration not currently supported")
	}
	if kf.Volumes != nil {
		return BuildOpts{}, fmt.Errorf("volumes configuration not currently supported")
	}
	if kf.Template != nil {
		return BuildOpts{}, fmt.Errorf("template configuration not currently supported")
	}

	if kf.Kernel != nil {
		opts.kconfig = slices.Clone(kf.Kernel.KConfig)
	}
	for _, target := range kf.Targets {
		opts.Platform = append(opts.Platform, opts.kconfigPlatform(target.Arch, target.Plat, target.KConfig))
	}

	for _, rom := range kf.Roms {
		if rom.Source == nil || rom.Source.Path == "" {
			return BuildOpts{}, fmt.Errorf("rom entry is missing a source path")
		}
		romFormat := cmp.Or(rom.Format, kraftfile.FsTypeErofs)
		romOpt := FSOpts{
			Source:     src,
			Path:       rom.Source.Path,
			Format:     romFormat,
			Type:       rom.Source.Type,
			Dockerfile: rom.Source.Dockerfile,
			// Pad the file to page-size alignment. This is required by the platform
			// which rejects ROM files that are not page-aligned.
			Pad: 4096,
		}
		if err := resolveSource(&romOpt); err != nil {
			return BuildOpts{}, fmt.Errorf("resolving rom source %q: %w", rom.Source.Path, err)
		}
		opts.Roms = append(opts.Roms, romOpt)
	}

	if kf.Rootfs != nil {
		if kf.Rootfs.Source == nil || kf.Rootfs.Source.Path == "" {
			return BuildOpts{}, fmt.Errorf("rootfs entry is missing a source path")
		}
		opts.Rootfs.Source = src
		opts.Rootfs.Path = kf.Rootfs.Source.Path
		opts.Rootfs.Format = kf.Rootfs.Format
		opts.Rootfs.Type = kf.Rootfs.Source.Type
		opts.Rootfs.Dockerfile = kf.Rootfs.Source.Dockerfile
		if err := resolveSource(&opts.Rootfs); err != nil {
			return BuildOpts{}, fmt.Errorf("resolving rootfs source %q: %w", kf.Rootfs.Source.Path, err)
		}
	}

	return opts, nil
}

// kraftfileKernel returns the host path of the already-built kernel that the
// kernel entry of kf names.
func kraftfileKernel(kf *kraftfile.Kraftfile, src Source) (string, error) {
	path := kf.Kernel.Source
	if path != "" && !strings.Contains(path, "://") {
		switch src := src.(type) {
		case nil:
		case LocalSource:
			path = src.join(path)
		case StateSource, SessionSource:
			return "", fmt.Errorf("kernel cannot come from %s", sourceKind(src))
		default:
			return "", fmt.Errorf("unsupported source %T", src)
		}
	}

	typ := cmp.Or(kf.Kernel.Type, kraftfile.KernelTypeSource)
	if kf.Kernel.Type == "" && path != "" {
		var err error
		typ, err = detectKernelType(path)
		if err != nil {
			return "", err
		}
	}
	if typ != kraftfile.KernelTypeKernel {
		return "", fmt.Errorf("building unikraft from source not currently supported")
	}
	if path == "" {
		return "", fmt.Errorf("kernel entry is missing a source path")
	}
	if kf.Runtime != nil {
		return "", fmt.Errorf("kernel and runtime configuration are mutually exclusive")
	}
	if kf.Kernel.Version != "" {
		return "", fmt.Errorf("kernel entry does not support a version")
	}
	return path, nil
}

// kconfigPlatform returns the platform arch/plat, with the kernel-level
// kconfig of o merged under the target's own.
func (o BuildOpts) kconfigPlatform(arch, plat string, target kraftfile.Map) ocispec.Platform {
	kconfig := slices.Clone(o.kconfig)
	kconfig.Merge(target)

	var features []string
	for _, kv := range kconfig {
		features = append(features, fmt.Sprintf("%s=%v", kv.Key, kv.Value))
	}
	var version string
	if value, ok := kconfig.Lookup("CONFIG_UK_FULLVERSION"); ok {
		version = fmt.Sprint(value)
	}
	return ocispec.Platform{
		Architecture: arch,
		OS:           plat,
		OSVersion:    version,
		OSFeatures:   features,
	}
}
