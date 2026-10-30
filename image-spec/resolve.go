// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"unikraft.com/x/image-spec/reference"
)

// Resolved is the result of resolving a location. Only a remote is ever
// resolved: a registry, or an origin serving a layout over HTTP, is pinned to
// what it names at the time, while a local layout or archive has nothing to
// resolve and is kept as given. It is what an [Accessor] loads, and what an
// [Image] carries to say where it was loaded from.
type Resolved struct {
	// Reference is the reference as requested, or zero for a local layout or
	// archive.
	Reference reference.Reference
	// ResolvedReference is the canonical name of the image where it was found,
	// or zero for a local layout or archive. For a registry it is Reference
	// with the tag it resolved, and the digest of Descriptor; for a layout
	// served over HTTP it is the URL the origin redirected to, or zero if it
	// served the tag directly. The digest in that URL is only the origin's
	// address for the tarball.
	ResolvedReference reference.Reference
	// Location is where Load reads from: the pinned location of a remote, or
	// the local layout or archive as given, which Resolve leaves alone.
	Location Location

	// Descriptor is the root index or manifest the image is loaded from: what
	// a registry or layout tag names, or a tarball's own index.json. Once set,
	// Load reads it instead of whatever Location names by then, and fails if
	// Location no longer has it. Resolve sets it only for a registry, which
	// answers with it; Load and Save always set it.
	Descriptor ocispec.Descriptor
}
