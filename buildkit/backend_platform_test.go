// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package buildkit

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unikraft.com/cloud/sdk/platform"
)

// fakePlatform answers every platform request with success, and sends the
// requests to create an instance on the returned channel.
func fakePlatform(t *testing.T) (platform.Client, <-chan platform.CreateInstanceRequest) {
	t.Helper()

	created := make(chan platform.CreateInstanceRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/instances" {
			var req platform.CreateInstanceRequest
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			created <- req
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status": "success"}`))
	}))
	t.Cleanup(srv.Close)

	return platform.NewClient(platform.WithDefaultEndpoint(srv.URL), platform.WithToken("token")), created
}

func TestPlatformCreate(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []Option
		key  Key
		want string
	}{
		{
			name: "the default cache volume is kept half free",
			want: "2147,10737,10737",
		},
		{
			name: "a smaller cache volume is kept half free too",
			opts: []Option{WithCacheSize(4096)},
			want: "429,2147,2147",
		},
		{
			name: "the key's cache volume is kept half free too",
			opts: []Option{WithCacheSize(8192)},
			key:  Key{CacheMB: 4096},
			want: "429,2147,2147",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, created := fakePlatform(t)
			require.NoError(t, NewPlatform(c, tc.opts...).create(t.Context(), "buildkit-default", tc.key))

			req := <-created
			assert.Equal(t, tc.want, req.Env[EnvOCIWorkerGCKeepStorage])
		})
	}
}

func TestPlatformCreateSized(t *testing.T) {
	c, created := fakePlatform(t)
	p := NewPlatform(c, WithImage("official/utils/buildkit:v1"), WithVCPUs(2), WithMemory(4096))
	require.NoError(t, p.create(t.Context(), "buildkit-ci", Key{Name: "ci", VCPUs: 8, AutokillMs: 60000}))

	// The key's sizes make the builder, and the options fill the rest.
	req := <-created
	assert.Equal(t, platform.ImageReference("official/utils/buildkit:v1"), req.Image)
	assert.Equal(t, int32(8), *req.Vcpus)
	assert.Equal(t, int64(4096), *req.MemoryMb)
	assert.Equal(t, uint64(60000), *req.Autokill.TimeMs)
	require.Len(t, req.Volumes, 1)
	assert.Equal(t, uint64(20480), *req.Volumes[0].SizeMb)
}

func TestPlatformCheck(t *testing.T) {
	// builder answers a request for the builder ci, running image.
	builder := func(image string) string {
		return fmt.Sprintf(`{"status": "success", "data": {"instances": [{"status": "success", "uuid": "u1", "name": "buildkit-ci", "state": "running", "image": %q, "vcpus": 2, "memory_mb": 4096, "volumes": [{"uuid": "v1", "name": "vol-abc", "at": "/var/lib/buildkit"}]}]}}`, image)
	}
	const (
		digest   = "official/utils/buildkit@sha256:3b4c6b2c2c7ab4a5e2a2d4b8b5ba4bb9a6a7e5a9d4d0d2c1a4f0b2c3d4e5f6a7"
		notFound = `{"status": "error", "data": {"instances": [{"status": "error", "message": "not found", "error": 8}]}}`
		cache    = `{"status": "success", "data": {"volumes": [{"status": "success", "uuid": "v1", "name": "vol-abc", "size_mb": 10240}]}}`
	)

	t.Run("a key without sizes asks the platform nothing", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			t.Errorf("unexpected platform request: %s %s", r.Method, r.URL.Path)
		}))
		t.Cleanup(srv.Close)
		c := platform.NewClient(platform.WithDefaultEndpoint(srv.URL), platform.WithToken("token"))

		assert.NoError(t, NewPlatform(c).Check(t.Context(), Key{Name: "ci"}))
	})

	for _, tc := range []struct {
		name string
		key  Key
		// answers holds the body for each "METHOD /path", else success.
		answers map[string]string
		// want is in the conflict, or empty for none.
		want string
	}{
		{
			name:    "a builder that does not exist is no conflict",
			key:     Key{Name: "ci", VCPUs: 4},
			answers: map[string]string{"GET /v1/instances": notFound},
		},
		{
			name:    "a builder of the key's sizes is no conflict",
			key:     Key{Name: "ci", Image: digest, VCPUs: 2, MemoryMB: 4096, CacheMB: 10240},
			answers: map[string]string{"GET /v1/instances": builder(digest), "GET /v1/volumes": cache},
		},
		{
			name:    "a builder with other vcpus is a conflict",
			key:     Key{Name: "ci", VCPUs: 4},
			answers: map[string]string{"GET /v1/instances": builder(digest)},
			want:    "has 2 vcpus, not 4",
		},
		{
			name:    "a builder with other memory is a conflict",
			key:     Key{Name: "ci", MemoryMB: 8192},
			answers: map[string]string{"GET /v1/instances": builder(digest)},
			want:    "has 4096 MiB of memory, not 8192",
		},
		{
			name:    "a cache volume of another size is a conflict",
			key:     Key{Name: "ci", CacheMB: 20480},
			answers: map[string]string{"GET /v1/instances": builder(digest), "GET /v1/volumes": cache},
			want:    "has a cache of 10240 MiB, not 20480",
		},
		{
			name:    "a tag against the resolved digest of the same image is no conflict",
			key:     Key{Name: "ci", Image: "official/utils/buildkit:latest"},
			answers: map[string]string{"GET /v1/instances": builder(digest)},
		},
		{
			name:    "another image is a conflict",
			key:     Key{Name: "ci", Image: "official/utils/buildkit-arm:latest"},
			answers: map[string]string{"GET /v1/instances": builder(digest)},
			want:    "runs " + digest + ", not official/utils/buildkit-arm:latest",
		},
		{
			name:    "another tag of the same image is a conflict",
			key:     Key{Name: "ci", Image: "official/utils/buildkit:v2"},
			answers: map[string]string{"GET /v1/instances": builder("official/utils/buildkit:v1")},
			want:    "runs official/utils/buildkit:v1, not official/utils/buildkit:v2",
		},
		{
			name:    "another registry is a conflict",
			key:     Key{Name: "ci", Image: "registry-a.example/team/buildkit:v1"},
			answers: map[string]string{"GET /v1/instances": builder("registry-b.example/team/buildkit:v1")},
			want:    "runs registry-b.example/team/buildkit:v1, not registry-a.example/team/buildkit:v1",
		},
		{
			name:    "the platform's own registry left out is no conflict",
			key:     Key{Name: "ci", Image: "index.unikraft.io/official/utils/buildkit:latest"},
			answers: map[string]string{"GET /v1/instances": builder(digest)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(cmp.Or(tc.answers[r.Method+" "+r.URL.Path], `{"status": "success"}`)))
			}))
			t.Cleanup(srv.Close)
			c := platform.NewClient(platform.WithDefaultEndpoint(srv.URL), platform.WithToken("token"))

			err := NewPlatform(c).Check(t.Context(), tc.key)
			if tc.want == "" {
				assert.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrConflict)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

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
				"GET /v1/instances":          `{"status": "success", "data": {"instances": [{"status": "success", "uuid": "u1", "name": "buildkit-default", "state": "standby"}]}}`,
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
			_, _ = w.Write([]byte(`{"status": "success", "data": {"instances": [{"status": "success", "uuid": "u1", "name": "buildkit-ci", "state": "running", "vcpus": 2, "memory_mb": 4096}]}}`))
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
