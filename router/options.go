// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package router

import (
	"time"

	"github.com/gin-gonic/gin"
)

// RouterOption is the prototype for defining method-based options for the
// router.
type RouterOption func(*Router) error

// WithGlobalMiddleware injects global middleware into the main router which
// will be used across all routes.
func WithGlobalMiddleware(middleware ...gin.HandlerFunc) RouterOption {
	return func(router *Router) error {
		router.middleware = append(router.middleware, middleware...)
		return nil
	}
}

// WithRoutes adds all the routes to the main router.
func WithRoutes(routes ...RouteHandler) RouterOption {
	return func(router *Router) error {
		router.routes = append(router.routes, routes...)
		return nil
	}
}

// WithDebug enables or disables debug mode for the router.
func WithDebug(debug bool) RouterOption {
	return func(router *Router) error {
		router.debug = debug
		return nil
	}
}

// WithReadTimeout sets the read timeout for the HTTP server.
func WithReadTimeout(timeout time.Duration) RouterOption {
	return func(router *Router) error {
		router.readTimeout = timeout
		return nil
	}
}

// WithWriteTimeout sets the write timeout for the HTTP server.
func WithWriteTimeout(timeout time.Duration) RouterOption {
	return func(router *Router) error {
		router.writeTimeout = timeout
		return nil
	}
}

// WithIdleTimeout sets the idle timeout for the HTTP server.
func WithIdleTimeout(timeout time.Duration) RouterOption {
	return func(router *Router) error {
		router.idleTimeout = timeout
		return nil
	}
}

// WithReadHeaderTimeout sets the read header timeout for the HTTP server.
func WithReadHeaderTimeout(timeout time.Duration) RouterOption {
	return func(router *Router) error {
		router.readHeaderTimeout = timeout
		return nil
	}
}

// WithShutdownTimeout sets the shutdown timeout for the HTTP server.
func WithShutdownTimeout(timeout time.Duration) RouterOption {
	return func(router *Router) error {
		router.shutdownTimeout = timeout
		return nil
	}
}

// WithMaxMultipartMemory sets the maximum memory used for multipart forms in
// the HTTP server.
func WithMaxMultipartMemory(maxMemory int64) RouterOption {
	return func(router *Router) error {
		router.maxMultipartMemory = maxMemory
		return nil
	}
}
