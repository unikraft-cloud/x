// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package router

import "github.com/gin-gonic/gin"

// AttachGlobalMiddleware injects global middleware to the main Go engine, and
// returns the result so that this method can be used during chaining.
func (router *Router) AttachGlobalMiddleware(handlers ...gin.HandlerFunc) gin.IRoutes {
	return router.engine.Use(handlers...)
}

// AttachNoRouteHandler sets a handler for unmatched routes, which will be
// called when no other route matches the request.
func (router *Router) AttachNoRouteHandler(handler gin.HandlerFunc) {
	router.engine.NoRoute(handler)
}

// AttachNoMethodHandler sets a handler for unmatched HTTP methods, which will
// be called when a request matches a route but the method does not match any
func (router *Router) AttachGroup(relativePath string, handlers ...gin.HandlerFunc) *gin.RouterGroup {
	return router.engine.Group(relativePath, handlers...)
}

// AttachHandler adds a handler for a specific HTTP method and path to the main
// Go engine.
func (router *Router) AttachHandler(method string, path string, handler gin.HandlerFunc) {
	router.engine.Handle(method, path, handler)
}
