// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package router

import (
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
