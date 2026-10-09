// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"unikraft.com/x/image-spec/reference"
)

// Resolved is a location pinned to the digest it named when it was resolved.
// It is what an [Accessor] loads, and what an [Image] carries to say where it
// came from.
type Resolved struct {
	// Reference is the reference as requested, or zero for a local layout or
	// archive.
	Reference reference.Reference
	// ResolvedReference names the image by digest where it was found, or zero
	// for a local layout or archive. For a registry it is Reference without its
	// tag and with its digest; for a layout served over HTTP it is the location
	// the origin redirected to.
	ResolvedReference reference.Reference
	// Location is where Load reads from: the pinned location of a remote, or
	// the local layout or archive as given, which Resolve leaves alone.
	Location Location

	// descriptor is what the registry answered Resolve with, kept so that Load
	// does not ask again. No other scheme sets it, since their root descriptor
	// is only known once the content is read.
	descriptor ocispec.Descriptor
}
