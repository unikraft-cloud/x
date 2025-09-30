// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package middleware

import "github.com/gin-gonic/gin"

// ExtraHeaders returns a new gin middleware which adds various extra headers to
// the response.
func ExtraHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Inform all callers which server implementation this is.
		c.Header("Server", "Unikraft Cloud")

		// Equivalent to CSP frame-ancestors for older browsers
		c.Header("X-Frame-Options", "DENY")

		// Don't do MIME type sniffing
		c.Header("X-Content-Type-Options", "nosniff")

		// Only send Referer header for URLs matching our protocol, hostname and port
		c.Header("Referrer-Policy", "same-origin")

		// Prevent google chrome cohort tracking. Originally this was referred
		// to as FlocBlock. Floc was replaced by Topics in 2022 and the spec says
		// that interest-cohort will also block Topics (as of 2022-Nov).
		//
		// See: https://smartframe.io/blog/google-topics-api-everything-you-need-to-know
		//
		// See: https://github.com/patcg-individual-drafts/topics
		c.Header("Permissions-Policy", "browsing-topics=()")

		// Some AI scrapers respect the following tags to opt-out
		// of their crawling and datasets.
		c.Header("X-Robots-Tag", "noimageai")
		// c.Header calls .Set(), but we want to emit the header
		// twice, not override it.
		c.Writer.Header().Add("X-Robots-Tag", "noai")
	}
}
