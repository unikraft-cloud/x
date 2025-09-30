// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package middleware

import (
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

// CORS returns a new gin middleware which allows CORS requests to be processed.
// This is necessary in order for web/browser-based clients like Semaphore to
// work.
func CORS() gin.HandlerFunc {
	cfg := cors.Config{
		// TODO(nderjung): Use config to customize this.
		AllowAllOrigins: true,

		// Adds the following:
		// - "chrome-extension://"
		// - "safari-extension://"
		// - "moz-extension://"
		// - "ms-browser-extension://"
		AllowBrowserExtensions: true,
		AllowMethods: []string{
			"POST",
			"PUT",
			"DELETE",
			"GET",
			"PATCH",
			"OPTIONS",
		},
		AllowHeaders: []string{
			// Basic CORS.
			"Origin",
			"Content-Length",
			"Content-Type",

			// Needed to pass oauth bearer tokens.
			"Authorization",

			// Some clients require this.
			"Idempotency-Key",

			// Needed for websocket upgrade requests.
			"Upgrade",
			"Sec-WebSocket-Extensions",
			"Sec-WebSocket-Key",
			"Sec-WebSocket-Protocol",
			"Sec-WebSocket-Version",
			"Connection",
		},
		AllowWebSockets: true,
		ExposeHeaders: []string{
			// Needed for accessing next/prev links when making GET timeline requests.
			"Link",

			// Needed so clients can handle rate limits.
			"X-RateLimit-Reset",
			"X-RateLimit-Limit",
			"X-RateLimit-Remaining",
			"X-Request-Id",

			// WebSocket stuff.
			"Connection",
			"Sec-WebSocket-Accept",
			"Upgrade",
		},
		MaxAge: 2 * time.Minute,
	}

	return cors.New(cfg)
}
