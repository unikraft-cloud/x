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
	"sync/atomic"
	"testing"

	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/image-spec/contentutil"
	"unikraft.com/x/image-spec/reference"
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

	dgst, _ := registryIndex(t, host, repo, tag)
	return dgst
}

// registryIndex fetches the index that host serves for repo:tag, with its digest.
func registryIndex(t *testing.T, host, repo, tag string) (digest.Digest, ocispec.Index) {
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
	var idx ocispec.Index
	require.NoError(t, json.Unmarshal(body, &idx))
	return digest.FromBytes(body), idx
}

// registryTags returns the tags that host lists for repo.
func registryTags(t *testing.T, host, repo string) []string {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+host+"/v2/"+repo+"/tags/list", nil)
	require.NoError(t, err)
	req.SetBasicAuth("alice", "s3cret")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list struct{ Tags []string }
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
	return list.Tags
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

// packerPushed pushes an image per platform to repo under a temporary tag, as
// the packer does, and returns what Build gives for them.
func packerPushed(t *testing.T, repo string) []*imagespec.Image {
	t.Helper()

	resolver := docker.NewResolver(docker.ResolverOptions{
		Hosts: docker.ConfigureDefaultRegistries(
			docker.WithPlainHTTP(docker.MatchAllHosts),
			docker.WithAuthorizer(alice.Authorizer()),
		),
	})
	var pushed []*imagespec.Image
	for _, img := range testImages(fcIntel, fcArm) {
		ref := repo + ":tmp-test-fc-" + img.Image.Architecture
		pusher, err := resolver.Pusher(t.Context(), ref)
		require.NoError(t, err)
		desc, err := imagespec.SaveManifest(t.Context(), contentutil.FromPusher(pusher), ref, img)
		require.NoError(t, err)
		parsed, err := reference.Parse(ref)
		require.NoError(t, err)
		pushed = append(pushed, &imagespec.Image{
			Reference:          parsed,
			ManifestDescriptor: desc,
			Image:              img.Image,
		})
	}
	return pushed
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

	t.Run("the index of what the packer pushed replaces its temporary tags", func(t *testing.T) {
		// A Harbor: a registry, and an API that removes tags.
		reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
		var removed []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/api/") {
				reg.ServeHTTP(w, r)
				return
			}
			if user, pass, _ := r.BasicAuth(); r.Method != http.MethodDelete || user != "alice" || pass != "s3cret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			removed = append(removed, r.URL.EscapedPath())
		}))
		t.Cleanup(srv.Close)
		host := strings.TrimPrefix(srv.URL, "http://")

		pushed := packerPushed(t, host+"/acme/team/app")

		b := registryBuilder(alice)
		b.Credentials = alice
		_, err := b.Save(t.Context(), &imagespec.Location{Scheme: schemes.OCI, Path: host + "/acme/team/app:v1"}, pushed...)
		require.NoError(t, err)
		assert.Equal(t, []string{
			"/api/v2.0/projects/acme/repositories/team%252Fapp/artifacts/tmp-test-fc-x86_64/tags/tmp-test-fc-x86_64",
			"/api/v2.0/projects/acme/repositories/team%252Fapp/artifacts/tmp-test-fc-arm64/tags/tmp-test-fc-arm64",
		}, removed)
	})

	t.Run("a registry that is no Harbor loses the temporary tags too", func(t *testing.T) {
		host := startRegistry(t)
		pushed := packerPushed(t, host+"/acme/app")

		b := registryBuilder(alice)
		b.Credentials = alice
		saved, err := b.Save(t.Context(), &imagespec.Location{Scheme: schemes.OCI, Path: host + "/acme/app:v1"}, pushed...)
		require.NoError(t, err)

		assert.Equal(t, []string{"v1"}, registryTags(t, host, "acme/app"))
		dgst, idx := registryIndex(t, host, "acme/app", "v1")
		assert.Equal(t, dgst, saved.Digest)
		assert.Equal(t, []ocispec.Descriptor{pushed[0].ManifestDescriptor, pushed[1].ManifestDescriptor}, idx.Manifests)
		assert.Equal(t, []ocispec.Platform{fcIntel, fcArm}, []ocispec.Platform{*idx.Manifests[0].Platform, *idx.Manifests[1].Platform})
	})

	t.Run("an index over a manifest that the registry lacks is not pushed", func(t *testing.T) {
		host := startRegistry(t)
		pushed := packerPushed(t, host+"/acme/app")

		b := registryBuilder(alice)
		b.Credentials = alice
		_, err := b.Save(t.Context(), &imagespec.Location{Scheme: schemes.OCI, Path: host + "/acme/other:v1"}, pushed...)
		require.ErrorContains(t, err, pushed[0].ManifestDescriptor.Digest.String())

		req, err := http.NewRequestWithContext(t.Context(), http.MethodHead, "http://"+host+"/v2/acme/other/manifests/v1", nil)
		require.NoError(t, err)
		req.SetBasicAuth("alice", "s3cret")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("a manifest that the registry refuses for a while is found", func(t *testing.T) {
		// A registry that refuses the first checks of a manifest, as Harbor does after a push.
		reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
		var refused atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead && strings.Contains(r.URL.Path, "/manifests/sha256:") && refused.Add(1) <= 3 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			reg.ServeHTTP(w, r)
		}))
		t.Cleanup(srv.Close)
		host := strings.TrimPrefix(srv.URL, "http://")
		pushed := packerPushed(t, host+"/acme/slow")

		saved, err := registryBuilder(alice).Save(t.Context(), &imagespec.Location{Scheme: schemes.OCI, Path: host + "/acme/slow:v1"}, pushed...)
		require.NoError(t, err)
		assert.Greater(t, refused.Load(), int32(3))
		dgst, _ := registryIndex(t, host, "acme/slow", "v1")
		assert.Equal(t, dgst, saved.Digest)
	})

	t.Run("a manifest that the registry forbids fails at once", func(t *testing.T) {
		reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
		var forbidden atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/manifests/sha256:") && r.Method != http.MethodPut {
				if r.Method == http.MethodHead {
					forbidden.Add(1)
				}
				w.WriteHeader(http.StatusForbidden)
				return
			}
			reg.ServeHTTP(w, r)
		}))
		t.Cleanup(srv.Close)
		host := strings.TrimPrefix(srv.URL, "http://")
		pushed := packerPushed(t, host+"/acme/denied")

		_, err := registryBuilder(alice).Save(t.Context(), &imagespec.Location{Scheme: schemes.OCI, Path: host + "/acme/denied:v1"}, pushed...)
		require.ErrorContains(t, err, "403")
		assert.Equal(t, int32(1), forbidden.Load())
	})

	t.Run("what the packer pushed is saved alone", func(t *testing.T) {
		host := startRegistry(t)
		pushed := packerPushed(t, host+"/acme/mixed")

		b := registryBuilder(alice)
		b.Credentials = alice
		_, err := b.Save(t.Context(), &imagespec.Location{Scheme: schemes.OCI, Path: host + "/acme/mixed:v1"},
			append(pushed, testImages(fcIntel)...)...)
		require.ErrorContains(t, err, "cannot be saved with other images")
		assert.Empty(t, registryTags(t, host, "acme/mixed"))
	})

	t.Run("a push that fails leaves no temporary tags", func(t *testing.T) {
		// A registry that refuses the index.
		reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/manifests/v1") {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			reg.ServeHTTP(w, r)
		}))
		t.Cleanup(srv.Close)
		host := strings.TrimPrefix(srv.URL, "http://")
		pushed := packerPushed(t, host+"/acme/app")
		require.Len(t, registryTags(t, host, "acme/app"), 2)

		_, err := registryBuilder(alice).Save(t.Context(), &imagespec.Location{Scheme: schemes.OCI, Path: host + "/acme/app:v1"}, pushed...)
		require.Error(t, err)
		assert.Empty(t, registryTags(t, host, "acme/app"))
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
