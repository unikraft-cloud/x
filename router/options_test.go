// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithTrustedProxies(t *testing.T) {
	tests := []struct {
		name       string
		proxies    []string
		remoteAddr string
		headers    map[string]string
		want       string
	}{
		{
			name:       "untrusted peer is the client",
			proxies:    []string{"10.1.0.0/16"},
			remoteAddr: "203.0.113.7:1234",
			headers:    map[string]string{"X-Forwarded-For": "198.51.100.1"},
			want:       "203.0.113.7",
		},
		{
			name:       "trusted peer appends the client",
			proxies:    []string{"10.1.0.0/16"},
			remoteAddr: "10.1.0.5:1234",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.7"},
			want:       "203.0.113.7",
		},
		{
			name:       "spoofed entries left of the appended client are ignored",
			proxies:    []string{"10.1.0.0/16"},
			remoteAddr: "10.1.0.5:1234",
			headers:    map[string]string{"X-Forwarded-For": "198.51.100.1, 203.0.113.7"},
			want:       "203.0.113.7",
		},
		{
			name:       "X-Real-IP is ignored",
			proxies:    []string{"10.1.0.0/16"},
			remoteAddr: "10.1.0.5:1234",
			headers:    map[string]string{"X-Forwarded-For": "garbage", "X-Real-IP": "198.51.100.1"},
			want:       "10.1.0.5",
		},
		{
			name:       "no proxies trusts no peer",
			proxies:    []string{},
			remoteAddr: "10.1.0.5:1234",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.7"},
			want:       "10.1.0.5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			r, err := New(context.Background(), "",
				WithTrustedProxies(tt.proxies...),
				WithRoutes(func(_ context.Context, engine *gin.Engine) error {
					engine.GET("/", func(c *gin.Context) { got = c.ClientIP() })
					return nil
				}),
			)
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			r.engine.ServeHTTP(httptest.NewRecorder(), req)

			assert.Equal(t, tt.want, got)
		})
	}
}

func TestWithTrustedProxiesRejectsInvalid(t *testing.T) {
	_, err := New(context.Background(), "", WithTrustedProxies("not-a-cidr"))
	assert.Error(t, err)
}
