// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

type CacheControlConfig struct {
	// Slice of Cache-Control directives, which will be
	// joined comma-separated and served as the value of
	// the Cache-Control header.
	//
	// If no directives are set, the Cache-Control header
	// will not be sent in the response at all.
	//
	// For possible Cache-Control directive values, see:
	// https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Cache-Control
	Directives []string

	// Slice of Vary header values, which will be joined
	// comma-separated and served as the value of the Vary
	// header in the response.
	//
	// If no Vary header values are supplied, then the
	// Vary header will be omitted in the response.
	//
	// https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Vary
	Vary []string
}

// CacheControl returns a new gin middleware which allows
// routes to control cache settings on response headers.
func CacheControl(config CacheControlConfig) gin.HandlerFunc {
	if len(config.Directives) == 0 {
		// No Cache-Control directives provided,
		// return empty/stub function.
		return func(c *gin.Context) {}
	}

	// Cache control is usually done on hot paths so
	// parse vars outside of the returned function.
	var (
		ccHeader   = strings.Join(config.Directives, ", ")
		varyHeader = strings.Join(config.Vary, ", ")
	)

	if varyHeader == "" {
		return func(c *gin.Context) {
			c.Header("Cache-Control", ccHeader)
		}
	}

	return func(c *gin.Context) {
		c.Header("Cache-Control", ccHeader)
		c.Header("Vary", varyHeader)
	}
}

// DefaultCacheControl returns a new gin middleware which sets a default
// Cache-Control header on all responses.
func DefaultCacheControl() gin.HandlerFunc {
	return CacheControl(CacheControlConfig{
		Directives: []string{"private", "max-age=120"},
		Vary:       []string{"Accept", "Accept-Encoding"},
	})
}
