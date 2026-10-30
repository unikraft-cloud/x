// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/image-spec/schemes"

	"unikraft.com/x/build"
	"unikraft.com/x/build/registryauth"
)

// alice is who the test registry lets push, with her password.
var alice = registryauth.Credentials(func(string) (string, string, error) {
	return "alice", "s3cret", nil
})

var (
	fcIntel = ocispec.Platform{OS: "fc", Architecture: "x86_64"}
	fcArm   = ocispec.Platform{OS: "fc", Architecture: "arm64"}
)

// testImages makes an image per platform, with the kernel and initrd that
// Build gives one.
func testImages(platforms ...ocispec.Platform) []*imagespec.Image {
	imgs := make([]*imagespec.Image, 0, len(platforms))
	for _, p := range platforms {
		imgs = append(imgs, imagespec.NewImage(
			imagespec.WithPlatform(p),
			imagespec.WithKernel(imagespec.NewStaticFile("kernel", []byte("kernel for "+p.Architecture))),
			imagespec.WithInitrd(imagespec.NewStaticFile("initrd", []byte("initrd for "+p.Architecture))),
		))
	}
	return imgs
}

// startRegistry serves a registry that only alice can use with her password,
// and returns its host.
func startRegistry(t *testing.T) string {
	t.Helper()

	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "alice" || pass != "s3cret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		reg.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	return strings.TrimPrefix(srv.URL, "http://")
}

// registryBuilder gives a Builder whose accessor reaches registries over plain
// HTTP with creds, as a server would with the caller's token.
func registryBuilder(creds registryauth.Credentials) build.Builder {
	resolver := docker.NewResolver(docker.ResolverOptions{
		Hosts: docker.ConfigureDefaultRegistries(
			docker.WithPlainHTTP(docker.MatchAllHosts),
			docker.WithAuthorizer(creds.Authorizer()),
		),
	})
	return build.Builder{
		Images: func(context.Context) (*imagespec.Accessor, error) {
			return imagespec.NewAccessor(imagespec.WithResolver(resolver)), nil
		},
	}
}

// registryDigest fetches the index that host serves for repo:tag and returns
// its digest.
func registryDigest(t *testing.T, host, repo, tag string) digest.Digest {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+host+"/v2/"+repo+"/manifests/"+tag, nil)
	require.NoError(t, err)
	req.SetBasicAuth("alice", "s3cret")
	req.Header.Set("Accept", ocispec.MediaTypeImageIndex)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return digest.FromBytes(body)
}

// layoutDigest returns the digest that the OCI layout at dir lists for tag.
func layoutDigest(t *testing.T, dir, tag string) digest.Digest {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dir, ocispec.ImageIndexFile))
	require.NoError(t, err)
	var idx ocispec.Index
	require.NoError(t, json.Unmarshal(data, &idx))
	for _, m := range idx.Manifests {
		if m.Annotations[ocispec.AnnotationRefName] == tag {
			return m.Digest
		}
	}
	require.Failf(t, "tag not in layout", "no manifest tagged %q in %s", tag, dir)
	return ""
}

func TestSave(t *testing.T) {
	t.Run("push to a registry with credentials", func(t *testing.T) {
		host := startRegistry(t)
		b := registryBuilder(alice)
		var steps stepRecorder
		b.Progress = &steps

		saved, err := b.Save(t.Context(),
			&imagespec.Location{Scheme: schemes.OCI, Path: host + "/acme/app"},
			testImages(fcIntel, fcArm)...)
		require.NoError(t, err)
		assert.Equal(t, host+"/acme/app:latest", saved.Ref)
		assert.Equal(t, []ocispec.Platform{fcIntel, fcArm}, saved.Platforms)

		// The registry resolves the tag to the index that Save reports.
		assert.Equal(t, registryDigest(t, host, "acme/app", "latest"), saved.Digest)
		assert.Equal(t, []string{"[push] " + host + "/acme/app:latest"}, steps.names)
	})

	t.Run("a local layout needs no accessor", func(t *testing.T) {
		dir := t.TempDir()
		b := build.Builder{
			Images: func(context.Context) (*imagespec.Accessor, error) {
				return nil, errors.New("images called")
			},
		}

		saved, err := b.Save(t.Context(),
			&imagespec.Location{Scheme: schemes.OCILayout, Path: dir + ":v1"},
			testImages(fcIntel)...)
		require.NoError(t, err)
		assert.Equal(t, dir+":v1", saved.Ref)
		assert.Equal(t, layoutDigest(t, dir, "v1"), saved.Digest)
	})
}
