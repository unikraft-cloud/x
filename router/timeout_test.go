// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package router

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serve sends one request with header through a started router with opts,
// and returns how long its context had left, or false for no deadline.
func serve(t *testing.T, header http.Header, opts ...RouterOption) (time.Duration, bool) {
	t.Helper()

	var left time.Duration
	var bounded bool
	route := func(_ context.Context, e *gin.Engine) error {
		e.GET("/", func(c *gin.Context) {
			if d, ok := c.Request.Context().Deadline(); ok {
				left, bounded = time.Until(d), true
			}
		})
		return nil
	}

	r, err := New(t.Context(), "127.0.0.1:0", append(opts, WithRoutes(route))...)
	require.NoError(t, err)
	require.NoError(t, r.Start(t.Context()))
	t.Cleanup(func() { assert.NoError(t, r.Stop(context.Background())) })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	maps.Copy(req.Header, header)
	r.server.Handler.ServeHTTP(httptest.NewRecorder(), req)

	return left, bounded
}

// Not in parallel: New sets gin's globals.
func TestRequestTimeout(t *testing.T) {
	t.Run("a request ends after the default timeout", func(t *testing.T) {
		left, bounded := serve(t, nil)
		require.True(t, bounded)
		assert.InDelta(t, DefaultRequestTimeout, left, float64(time.Minute))
	})

	t.Run("the timeout is an option", func(t *testing.T) {
		left, bounded := serve(t, nil, WithRequestTimeout(time.Minute))
		require.True(t, bounded)
		assert.InDelta(t, time.Minute, left, float64(10*time.Second))
	})

	t.Run("a timeout that is not positive bounds no request", func(t *testing.T) {
		_, bounded := serve(t, nil, WithRequestTimeout(0))
		assert.False(t, bounded)
	})

	t.Run("an upgraded request is never bounded", func(t *testing.T) {
		_, bounded := serve(t, http.Header{"Upgrade": {"websocket"}})
		assert.False(t, bounded)
	})
}
