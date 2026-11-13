// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/image-spec/reference"

	"unikraft.com/x/build/registryauth"
)

func TestPush(t *testing.T) {
	realm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		if user != "alice" || pass != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"token":"t0k3n"}`)
	}))
	t.Cleanup(realm.Close)

	var mu sync.Mutex
	var mounts []string
	has := map[string]bool{}
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t0k3n" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm.URL+`",service="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// The registry shares blobs between repositories and cannot mount. Scope
		// its HEADs, so that each repository gets a blob by mount or upload.
		mu.Lock()
		parts := strings.Split(r.URL.Path, "/")
		if r.Method == http.MethodHead && len(parts) == 5 && parts[3] == "blobs" && !has[parts[2]+parts[4]] {
			mu.Unlock()
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if mount := r.URL.Query().Get("mount"); mount != "" {
			mounts = append(mounts, r.URL.RawQuery)
			has[parts[2]+mount] = true
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			return
		}
		mu.Unlock()
		// The registry holds its lock while it reads an upload, which a copied blob
		// streams from the registry itself.
		body, _ := io.ReadAll(r.Body) // the registry checks the digest
		r.Body = io.NopCloser(bytes.NewReader(body))
		reg.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")

	// Seed a runtime of two platforms, whose x86_64 kernel the packer takes.
	platform := ocispec.Platform{OS: "fc", Architecture: "x86_64"}
	named, err := reference.ParseNormalizedNamed(host + "/runtime:v1")
	require.NoError(t, err)
	seed := docker.NewResolver(docker.ResolverOptions{Hosts: docker.ConfigureDefaultRegistries(
		docker.WithPlainHTTP(docker.MatchAllHosts),
		docker.WithAuthorizer(docker.NewDockerAuthorizer(docker.WithAuthCreds(func(string) (string, string, error) {
			return "alice", "s3cret", nil
		}))),
	)})
	_, _, err = imagespec.SaveRegistryImage(t.Context(), named, seed,
		imagespec.NewImage(
			imagespec.WithPlatform(ocispec.Platform{OS: "fc", Architecture: "arm64"}),
			imagespec.WithKernel(imagespec.NewStaticFile("kernel", []byte("kernel-arm64"))),
		),
		imagespec.NewImage(
			imagespec.WithPlatform(platform),
			imagespec.WithKernel(imagespec.NewStaticFile("kernel", []byte("kernel-x86_64"))),
		),
	)
	require.NoError(t, err)

	// Serve the auth socket the packer fetches tokens from.
	sock := filepath.Join(t.TempDir(), "auth.sock")
	l, err := net.Listen("unix", sock)
	require.NoError(t, err)
	grpcSrv := grpc.NewServer()
	registryauth.Credentials(func(string) (string, string, error) {
		return "alice", "s3cret", nil
	}).Attachable().Register(grpcSrv)
	go func() { _ = grpcSrv.Serve(l) }()
	t.Cleanup(grpcSrv.Stop)

	dir := t.TempDir()
	cfg, err := json.Marshal(ocispec.Image{Platform: platform, Config: ocispec.ImageConfig{Cmd: []string{"/app"}}})
	require.NoError(t, err)
	cfgFile := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(cfgFile, cfg, 0o644))
	initrdFile := filepath.Join(dir, "initrd")
	require.NoError(t, os.WriteFile(initrdFile, []byte("initrd"), 0o644))
	out := filepath.Join(dir, "out.json")

	err = run([]string{
		"push", "--runtime", host + "/runtime:v1", "--config", cfgFile, "--auth-socket", sock,
		initrdFile, out, host + "/app:tmp-x86_64", host + "/other:tmp-x86_64",
	})
	require.NoError(t, err)

	var desc ocispec.Descriptor
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &desc))
	assert.Equal(t, ocispec.MediaTypeImageManifest, desc.MediaType)
	assert.Equal(t, platform, *desc.Platform)

	get := func(path string) []byte {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/v2/"+path, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer t0k3n")
		req.Header.Set("Accept", ocispec.MediaTypeImageManifest+","+ocispec.MediaTypeImageIndex)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, path)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return body
	}
	body := get("app/manifests/tmp-x86_64")
	assert.Equal(t, desc.Digest, digest.FromBytes(body))
	assert.Equal(t, desc.Digest, digest.FromBytes(get("other/manifests/tmp-x86_64")))

	var mfst ocispec.Manifest
	require.NoError(t, json.Unmarshal(body, &mfst))
	require.Len(t, mfst.Layers, 2)
	assert.Contains(t, string(get("app/blobs/"+mfst.Layers[0].Digest.String())), "kernel-x86_64")
	assert.Contains(t, string(get("app/blobs/"+mfst.Layers[1].Digest.String())), "initrd")

	var img ocispec.Image
	require.NoError(t, json.Unmarshal(get("app/blobs/"+mfst.Config.Digest.String()), &img))
	assert.Equal(t, []string{"/app"}, img.Config.Cmd)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, mounts, "mount="+mfst.Layers[0].Digest.String()+"&from=runtime")
}
