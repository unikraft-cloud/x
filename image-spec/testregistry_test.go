// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/opencontainers/go-digest"
)

// testRegistry is a registry in memory for a containerd resolver, whose repositories share all content.
type testRegistry struct {
	mu        sync.Mutex
	blobs     map[digest.Digest][]byte
	manifests map[digest.Digest]testManifest
	tags      map[string]digest.Digest // by repository:tag
	uploads   int
	puts      []string      // the reference of each manifest PUT
	refused   digest.Digest // a manifest that the registry refuses
}

type testManifest struct {
	mediaType string
	data      []byte
}

// startTestRegistry serves a testRegistry, and returns it with its host and a resolver of it.
func startTestRegistry(t *testing.T) (*testRegistry, string, remotes.Resolver) {
	t.Helper()

	reg := &testRegistry{
		blobs:     map[digest.Digest][]byte{},
		manifests: map[digest.Digest]testManifest{},
		tags:      map[string]digest.Digest{},
	}
	srv := httptest.NewServer(reg)
	t.Cleanup(srv.Close)

	resolver := docker.NewResolver(docker.ResolverOptions{
		Hosts: docker.ConfigureDefaultRegistries(docker.WithPlainHTTP(docker.MatchAllHosts)),
	})
	return reg, strings.TrimPrefix(srv.URL, "http://"), resolver
}

// tag returns the digest that repo:tag names, or "".
func (reg *testRegistry) tag(repo, tag string) digest.Digest {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return reg.tags[repo+":"+tag]
}

// manifestPuts returns the reference of each manifest PUT, in order.
func (reg *testRegistry) manifestPuts() []string {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return slices.Clone(reg.puts)
}

// refuse makes the registry refuse each PUT of the manifest dgst.
func (reg *testRegistry) refuse(dgst digest.Digest) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.refused = dgst
}

// has reports whether the registry holds dgst, as a blob or a manifest.
func (reg *testRegistry) has(dgst digest.Digest) bool {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	_, blob := reg.blobs[dgst]
	_, mfst := reg.manifests[dgst]
	return blob || mfst
}

func (reg *testRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	path := strings.TrimPrefix(r.URL.Path, "/v2/")
	if path == "" {
		return
	}
	if repo, rest, ok := strings.Cut(path, "/blobs/uploads/"); ok {
		reg.upload(w, r, repo, rest)
		return
	}
	if repo, ref, ok := strings.CutLast(path, "/manifests/"); ok {
		reg.manifest(w, r, repo, ref)
		return
	}
	if _, dgst, ok := strings.CutLast(path, "/blobs/"); ok {
		data, ok := reg.blobs[digest.Digest(dgst)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		serveContent(w, r, "application/octet-stream", data)
		return
	}
	http.NotFound(w, r)
}

func (reg *testRegistry) upload(w http.ResponseWriter, r *http.Request, repo, id string) {
	switch {
	case r.Method == http.MethodPost && id == "":
		reg.uploads++
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%d", repo, reg.uploads))
		w.WriteHeader(http.StatusAccepted)
	case r.Method == http.MethodPut:
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		dgst := digest.Digest(r.URL.Query().Get("digest"))
		if dgst != digest.FromBytes(data) {
			http.Error(w, "digest mismatch", http.StatusBadRequest)
			return
		}
		reg.blobs[dgst] = data
		w.Header().Set("Docker-Content-Digest", dgst.String())
		w.WriteHeader(http.StatusCreated)
	default:
		http.Error(w, "unsupported upload", http.StatusMethodNotAllowed)
	}
}

func (reg *testRegistry) manifest(w http.ResponseWriter, r *http.Request, repo, ref string) {
	if r.Method == http.MethodPut {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		dgst := digest.FromBytes(data)
		reg.puts = append(reg.puts, ref)
		if dgst == reg.refused {
			http.Error(w, "manifest refused", http.StatusBadRequest)
			return
		}
		if d, err := digest.Parse(ref); err == nil && d != dgst {
			http.Error(w, "digest mismatch", http.StatusBadRequest)
			return
		} else if err != nil {
			reg.tags[repo+":"+ref] = dgst
		}
		reg.manifests[dgst] = testManifest{r.Header.Get("Content-Type"), data}
		w.Header().Set("Docker-Content-Digest", dgst.String())
		w.WriteHeader(http.StatusCreated)
		return
	}

	dgst, err := digest.Parse(ref)
	if err != nil {
		dgst = reg.tags[repo+":"+ref]
	}
	mfst, ok := reg.manifests[dgst]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Docker-Content-Digest", dgst.String())
	serveContent(w, r, mfst.mediaType, mfst.data)
}

func serveContent(w http.ResponseWriter, r *http.Request, mediaType string, data []byte) {
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}
