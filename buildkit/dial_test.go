// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !js

package buildkit

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"unikraft.com/cloud/sdk/platform"
)

func TestPlatformAcquireQuota(t *testing.T) {
	// quotaError is the answer to a request over the quota, for a list of instances.
	const quotaError = `{"status": "error", "data": {"instances": [{"status": "error", "message": "Quota exceeded", "error": 19}]}}`

	for _, tc := range []struct {
		name string
		// answers holds the body for each "METHOD /path", else success.
		answers map[string]string
	}{
		{
			name:    "a new builder over the quota is refused",
			answers: map[string]string{"POST /v1/instances": quotaError},
		},
		{
			name: "a builder in standby over the quota is refused",
			answers: map[string]string{
				"GET /v1/instances":          `{"status": "success", "data": {"instances": [{"status": "success", "uuid": "u1", "name": "remote-builder-default", "state": "standby"}]}}`,
				"PUT /v1/instances/u1/start": quotaError,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(cmp.Or(tc.answers[r.Method+" "+r.URL.Path], `{"status": "success"}`)))
			}))
			t.Cleanup(srv.Close)
			c := platform.NewClient(platform.WithDefaultEndpoint(srv.URL), platform.WithToken("token"))

			_, err := NewPlatform(c).Acquire(t.Context(), Key{})
			require.ErrorIs(t, err, ErrQuota)
		})
	}
}

func TestPlatformAcquireConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/instances" {
			_, _ = w.Write([]byte(`{"status": "success", "data": {"instances": [{"status": "success", "uuid": "u1", "name": "remote-builder-ci", "state": "running", "vcpus": 2, "memory_mb": 4096}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status": "success"}`))
	}))
	t.Cleanup(srv.Close)
	c := platform.NewClient(platform.WithDefaultEndpoint(srv.URL), platform.WithToken("token"))

	// The builder Acquire finds must be at the key's sizes, as for Check.
	_, err := NewPlatform(c).Acquire(t.Context(), Key{Name: "ci", VCPUs: 4})
	require.ErrorIs(t, err, ErrConflict)
}

func TestPlatformAcquireStopped(t *testing.T) {
	certs, err := generateCerts()
	require.NoError(t, err)
	env, err := json.Marshal(certs)
	require.NoError(t, err)
	// The builder starts, then the platform stops it: the image pull failed.
	const inst = `{"status": "success", "data": {"instances": [{"status": "success", "uuid": "u1", "name": "remote-builder-default", "state": %q, %s "service_group": {"domains": [{"fqdn": "127.0.0.1"}]}, "env": %s}]}}`
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method != http.MethodGet || r.URL.Path != "/v1/instances":
			_, _ = w.Write([]byte(`{"status": "success"}`))
		case gets.Add(1) == 1:
			_, _ = fmt.Fprintf(w, inst, "starting", "", env)
		default:
			_, _ = fmt.Fprintf(w, inst, "standby", `"stopped_at": "2026-01-01T00:00:00Z", "stop_reason": 4, "stop_code": 1,`, env)
		}
	}))
	t.Cleanup(srv.Close)
	c := platform.NewClient(platform.WithDefaultEndpoint(srv.URL), platform.WithToken("token"))

	_, err = NewPlatform(c).Acquire(t.Context(), Key{})
	require.ErrorContains(t, err, "waiting for builder remote-builder-default: the builder stopped: platform stop: image pull failed")
}
