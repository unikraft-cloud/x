// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"fmt"
	"slices"

	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// RuntimeKind is what runs an image.
type RuntimeKind int

const (
	// RuntimeKindUnikraft runs the kernel, initrd and ROMs of an image.
	RuntimeKindUnikraft RuntimeKind = iota
)

func (k RuntimeKind) String() string {
	switch k {
	case RuntimeKindUnikraft:
		return "unikraft"
	}
	return fmt.Sprintf("RuntimeKind(%d)", int(k))
}

// oses returns the OSes of the images that k runs, in the order Platforms
// prefers them.
func (k RuntimeKind) oses() []string {
	switch k {
	case RuntimeKindUnikraft:
		// An old Unikraft image has no OS in its config.
		return []string{PlatformKraftcloud, PlatformFirecracker, PlatformQemu, ""}
	}
	return nil
}

var runtimeKinds = []RuntimeKind{RuntimeKindUnikraft}

// architectures are the architectures that Platforms matches, in the order it
// prefers them.
var architectures = []string{ArchitectureIntel, ArchitectureArm}

// Platforms matches the platforms that runtimes run, on any architecture
// NormalizeArch knows, or those of every known runtime if none is given, so
// that a load skips what nothing runs, such as an attestation. It orders them
// by the runtime, then the OS, then the architecture. A caller can still pass
// its own matcher instead.
func Platforms(runtimes ...RuntimeKind) platforms.MatchComparer {
	if len(runtimes) == 0 {
		runtimes = runtimeKinds
	}
	return runtimePlatforms(slices.Clone(runtimes))
}

type runtimePlatforms []RuntimeKind

func (r runtimePlatforms) Match(p ocispec.Platform) bool {
	return r.rank(p) >= 0
}

func (r runtimePlatforms) Less(p1, p2 ocispec.Platform) bool {
	a, b := r.rank(p1), r.rank(p2)
	return a >= 0 && (b < 0 || a < b)
}

// rank is the place of p in the order of r, or -1 if r does not match it.
func (r runtimePlatforms) rank(p ocispec.Platform) int {
	arch, err := NormalizeArch(p.Architecture)
	if err != nil {
		return -1
	}
	offset := 0
	for _, k := range r {
		oses := k.oses()
		if i := slices.Index(oses, p.OS); i >= 0 {
			return (offset+i)*len(architectures) + slices.Index(architectures, arch)
		}
		offset += len(oses)
	}
	return -1
}
