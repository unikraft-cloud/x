// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package schemes names the URI schemes an image is addressed by.
package schemes

import (
	"errors"
	"fmt"
)

// Scheme is the URI scheme an image is addressed by.
type Scheme string

const (
	// OCI addresses an image in an OCI registry.
	OCI Scheme = "oci"

	// OCILayout and OCIArchive address an image on the local
	// filesystem, as a directory and as a tarball respectively.
	OCILayout  Scheme = "oci-layout"
	OCIArchive Scheme = "oci-archive"

	// HTTPOCI and HTTPSOCI address an OCI image layout tarball
	// served over HTTP and HTTPS respectively.
	HTTPOCI  Scheme = "http+oci"
	HTTPSOCI Scheme = "https+oci"
)

// ErrUnsupported is returned for a URI scheme that does not name an
// image that can be addressed.
var ErrUnsupported = errors.New("unsupported image URI scheme")

// Parse returns the Scheme named by s.
func Parse(s string) (Scheme, error) {
	switch scheme := Scheme(s); scheme {
	case OCI, OCILayout, OCIArchive, HTTPOCI, HTTPSOCI:
		return scheme, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupported, s)
	}
}

// IsHTTP reports whether s addresses a layout served over HTTP.
func (s Scheme) IsHTTP() bool {
	return s == HTTPOCI || s == HTTPSOCI
}
