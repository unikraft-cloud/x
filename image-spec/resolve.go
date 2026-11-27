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
// resolved: a registry is pinned to what it names at the time, and an origin
// serving a layout over HTTP is followed to the URL it serves it at, while a
// local layout or archive has nothing to resolve and is kept as given. It is
// what an [Accessor] loads, and what an [Image] carries to say where it was
// loaded from.
type Resolved struct {
	// Reference is the reference as requested, or zero for a local layout or
	// archive.
	Reference reference.Reference
	// ResolvedReference is the canonical name of the image where it was found,
	// or zero for a local layout or archive. For a registry it is Reference
	// with the tag it resolved, and the digest of Descriptor; for a layout
	// served over HTTP it is the URL the origin serves it at, after its
	// redirects, and has no Digest, even if its path names one. That URL can
	// carry a query the origin put there, such as a signature, so it should
	// not be logged or shown to a user as is.
	ResolvedReference reference.Reference
	// Location is where Load reads from: the resolved location of a remote, or
	// the local layout or archive as given, which Resolve leaves alone.
	Location Location

	// Descriptor is the root index or manifest the image is loaded from: what
	// a tag names in a registry, or in the index.json of a layout or tarball,
	// or the image a tarball served over HTTP holds, and never that index.json
	// itself. Once set, Load reads it instead of whatever Location names by
	// then, and fails if Location no longer has it. Resolve sets it only for a
	// registry, which answers with it; Load and Save always set it.
	Descriptor ocispec.Descriptor
}
