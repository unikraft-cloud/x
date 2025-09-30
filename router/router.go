// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package router

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"codeberg.org/gruf/go-bytesize"
	"github.com/gin-gonic/gin"

	"unikraft.com/x/log"
)

const (
	ReadTimeout        = 60 * time.Second
	WriteTimeout       = 30 * time.Second
	IdleTimeout        = 30 * time.Second
	ReadHeaderTimeout  = 30 * time.Second
	ShutdownTimeout    = 30 * time.Second
	MaxMultipartMemory = int64(8 * bytesize.MiB)
)

// RouteHandler defines the prototype for adding routes to the main router.
type RouteHandler func(context.Context, *gin.Engine) error

// Router provides the HTTP REST interface for the core application using gin.
type Router struct {
	debug      bool
	engine     *gin.Engine
	middleware []gin.HandlerFunc
	routes     []RouteHandler
	server     *http.Server
	running    atomic.Bool
}

// New returns a new router, which wraps an http server and gin handler engine.
//
// When the router's work is finished, Stop should be called on it to close
// connections gracefully.
func New(ctx context.Context, addr string, opts ...RouterOption) (*Router, error) {
	router := Router{}

	// Apply all method options.
	for _, opt := range opts {
		if err := opt(&router); err != nil {
			return nil, fmt.Errorf("applying router options: %w", err)
		}
	}

	// Set Gin mode.
	if router.debug {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	// Create a new gin engine instance (now that we have the mode set).
	router.engine = gin.New()

	// Create the engine here -- this is the core request routing handler.
	router.engine.MaxMultipartMemory = MaxMultipartMemory
	router.engine.HandleMethodNotAllowed = true

	router.server = &http.Server{
		Addr:              addr,
		Handler:           router.engine,
		ReadTimeout:       ReadTimeout,
		ReadHeaderTimeout: ReadHeaderTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       IdleTimeout,
	}

	// Attach global middlewares which are used for every request.
	router.engine.Use(router.middleware...)

	// Register all route handlers.
	for _, init := range router.routes {
		if err := init(ctx, router.engine); err != nil {
			return nil, fmt.Errorf("initializing router handler: %w", err)
		}
	}

	return &router, nil
}

// Start the HTTP server and blocks until it is stopped or an error occurs.
// It uses the provided context to set the base context for the server, which
// allows for graceful shutdown and cancellation of requests.
//
// The provided context will be used as the base context for all requests
// passing through the underlying http.Server, so this should be a long-running
// context.
func (router *Router) Start(ctx context.Context) error {
	if router.running.Load() {
		return fmt.Errorf("server is already running")
	}

	if mux, ok := router.server.Handler.(*gin.Engine); ok {
		mux.HTMLRender = &HTMLTemplRenderer{
			ctx:      ctx,
			fallback: mux.HTMLRender,
		}

		// Wrap the gin engine handler in our own timeout handler, to ensure we don't
		// keep very slow requests around.
		router.server.Handler = timeoutHandler{mux}
	}

	// Set the base context for the server.
	router.server.BaseContext = func(net.Listener) context.Context {
		return ctx
	}

	log.G(ctx).
		Info().
		Str("addr", router.server.Addr).
		Msg("starting server")

	ln, err := net.Listen("tcp", router.server.Addr)
	if err != nil {
		return err
	}

	// Start listening on a separate thread and save the error if it occurs.
	go func() {
		if err := router.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.G(ctx).
				Error().
				Err(err).
				Msg("server failed to start")
		}
		router.running.Store(false)
	}()

	// Set the running flag to true to indicate that the server is now running.
	router.running.Store(true)

	return nil
}

// Stop gracefully stops the HTTP server, allowing for ongoing requests to
// finish before shutting down.
func (router *Router) Stop(ctx context.Context) error {
	if !router.running.Load() {
		return nil
	}

	log.G(ctx).
		Info().
		Msg("stopping server")

	ctx, cancel := context.WithTimeout(ctx, ShutdownTimeout)
	defer cancel()

	if err := router.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutting down server: %w", err)
	}

	// Reset the server and engine to their initial state.
	router.running.Store(false)

	log.G(ctx).
		Info().
		Msg("server stopped successfully")

	return nil
}
