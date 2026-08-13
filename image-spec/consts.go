// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"fmt"
	"strings"
)

const (
	WellKnownKernelPath      = "/unikraft/bin/kernel"
	WellKnownKernelDbgPath   = "/unikraft/bin/kernel.dbg"
	WellKnownInitrdPath      = "/unikraft/bin/initrd"
	WellKnownKConfigPath     = "/unikraft/bin/config"
	WellKnownConfigPath      = "/unikraft/config.json"
	WellKnownCmdlinePath     = "/unikraft/cmdline.txt"
	WellKnownKernelSourceDir = "/unikraft/src"
	WellKnownAppSourceDir    = "/unikraft/app"
)

const (
	AnnotationMediaType            = "org.unikraft.mediaType"
	AnnotationName                 = "org.unikraft.image.name"
	AnnotationVersion              = "org.unikraft.image.version"
	AnnotationURL                  = "org.unikraft.image.url"
	AnnotationCreated              = "org.unikraft.image.created"
	AnnotationDescription          = "org.unikraft.image.description"
	AnnotationKernelPath           = "org.unikraft.kernel.image"
	AnnotationKernelDbgPath        = "org.unikraft.kernel.imagedbg"
	AnnotationKernelVersion        = "org.unikraft.kernel.version"
	AnnotationKernelInitrdPath     = "org.unikraft.kernel.initrd"
	AnnotationKernelKConfig        = "org.unikraft.kernel.kconfig."
	AnnotationKernelArch           = "org.unikraft.kernel.arch"
	AnnotationKernelPlat           = "org.unikraft.kernel.plat"
	AnnotationFilesystemPath       = "org.unikraft.filesystem"
	AnnotationDiskIndexPathPattern = "org.unikraft.disk-%d"
	AnnotationKraftKitVersion      = "sh.kraftkit.version"
)

const (
	MediaTypeKernel = "application/vnd.unikraft.kernel.v1"
	MediaTypeInitrd = "application/vnd.unikraft.initrd.v1"
	MediaTypeRom    = "application/vnd.unikraft.rom.v1"
)

const (
	ArchitectureIntel = "x86_64"
	ArchitectureArm   = "arm64"
)

func NormalizeArch(arch string) (string, error) {
	switch strings.ToLower(arch) {
	case "x86_64", "x86-64", "amd64":
		return ArchitectureIntel, nil
	case "arm64", "aarch64":
		return ArchitectureArm, nil
	default:
		return "", fmt.Errorf("unsupported architecture %q", arch)
	}
}

const (
	PlatformKraftcloud  = "kraftcloud"
	PlatformFirecracker = "fc"
	PlatformQemu        = "qemu"
)
