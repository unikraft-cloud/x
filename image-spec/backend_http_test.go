// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/platforms"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zlib"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/image-spec/schemes"
)

// serveTestTarball serves a layout tarball, addressed by the digest of its
// bytes, and also returns its root.
func serveTestTarball(t *testing.T) (*httptest.Server, digest.Digest, ocispec.Descriptor) {
	t.Helper()
	tarPath := filepath.Join(t.TempDir(), "image.tar")
	root, err := SaveTarball(t.Context(), tarPath, testImage)
	require.NoError(t, err)
	data, err := os.ReadFile(tarPath)
	require.NoError(t, err)
	dgst := digest.FromBytes(data)

	mux := http.NewServeMux()
	serve := func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") != "yes" {
			http.Error(w, "missing header", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", MediaTypeOCILayoutTar)
		_, _ = w.Write(data)
	}
	mux.HandleFunc("/ns/direct/latest", serve)
	mux.HandleFunc("/ns/img/@"+dgst.String(), serve)
	mux.HandleFunc("/ns/redirect/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ns/img/@"+dgst.String(), http.StatusFound)
	})
	// http.Redirect would make these absolute, so set the location by hand.
	mux.HandleFunc("/ns/img/relative", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "./@"+dgst.String())
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/ns/other/parent", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "../img/@"+dgst.String())
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/ns/gzip/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", MediaTypeOCILayoutTar)
		w.Header().Set("Content-Encoding", "gzip")
		gw := gzip.NewWriter(w)
		_, _ = gw.Write(data)
		_ = gw.Close()
	})
	mux.HandleFunc("/ns/deflate/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", MediaTypeOCILayoutTar)
		w.Header().Set("Content-Encoding", "deflate")
		zw := zlib.NewWriter(w)
		_, _ = zw.Write(data)
		_ = zw.Close()
	})
	mux.HandleFunc("/ns/bad/@"+digest.FromString("other").String(), serve)
	mux.HandleFunc("/ns/denied/@"+dgst.String(), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	})
	mux.HandleFunc("/ns/soft404/latest", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>not found</html>"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, dgst, root
}

func TestHTTPAccessor(t *testing.T) {
	srv, dgst, root := serveTestTarball(t)
	host := srv.Listener.Addr().String()

	tests := []struct {
		name         string
		path         string
		wantLocation string
		wantPinned   bool
	}{
		{
			name:         "redirect",
			path:         "/ns/redirect/latest",
			wantLocation: host + "/ns/img/@" + dgst.String(),
			wantPinned:   true,
		},
		{
			name:         "redirect-relative",
			path:         "/ns/img/relative",
			wantLocation: host + "/ns/img/@" + dgst.String(),
			wantPinned:   true,
		},
		{
			name:         "redirect-parent",
			path:         "/ns/other/parent",
			wantLocation: host + "/ns/img/@" + dgst.String(),
			wantPinned:   true,
		},
		{
			name:         "gzip",
			path:         "/ns/gzip/latest",
			wantLocation: host + "/ns/gzip/latest",
		},
		{
			name:         "deflate",
			path:         "/ns/deflate/latest",
			wantLocation: host + "/ns/deflate/latest",
		},
		{
			name:         "direct",
			path:         "/ns/direct/latest",
			wantLocation: host + "/ns/direct/latest",
		},
		{
			name:         "digest",
			path:         "/ns/img/@" + dgst.String(),
			wantLocation: host + "/ns/img/@" + dgst.String(),
			wantPinned:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			loc, err := ParseLocation("http+oci://" + host + tt.path)
			require.NoError(t, err)

			accessor := NewAccessor(WithRegistryHeaders(http.Header{"X-Test": {"yes"}}))
			res, err := accessor.Resolve(ctx, loc)
			require.NoError(t, err)
			require.Equal(t, tt.wantLocation, res.Location.Path)
			require.Equal(t, tt.wantPinned, res.ResolvedReference.Digest() != "")
			require.Zero(t, res.Descriptor, "only the download knows the root")

			img, err := accessor.Load(ctx, res, platforms.All)
			require.NoError(t, err)
			require.NoError(t, img.Close())
			require.Equal(t, root, img.Descriptor)

			again, err := accessor.Load(ctx, img.Resolved, platforms.All)
			require.NoError(t, err)
			require.Equal(t, img.Resolved, again.Resolved)
			require.NoError(t, again.Close())
		})
	}
}

func TestHTTPAccessorIgnoresURLDigest(t *testing.T) {
	srv, _, root := serveTestTarball(t)
	loc, err := ParseLocation("http+oci://" + srv.Listener.Addr().String() + "/ns/bad/@" + digest.FromString("other").String())
	require.NoError(t, err)

	accessor := NewAccessor(WithRegistryHeaders(http.Header{"X-Test": {"yes"}}))
	res, err := accessor.Resolve(t.Context(), loc)
	require.NoError(t, err)
	img, err := accessor.Load(t.Context(), res, platforms.All)
	require.NoError(t, err)
	require.Equal(t, root, img.Descriptor)
	require.NoError(t, img.Close())
}

func TestHTTPAccessorLoadPinned(t *testing.T) {
	srv, _, root := serveTestTarball(t)
	accessor := NewAccessor(WithRegistryHeaders(http.Header{"X-Test": {"yes"}}))
	direct := Location{Scheme: schemes.HTTPOCI, Path: srv.Listener.Addr().String() + "/ns/direct/latest"}

	img, err := accessor.Load(t.Context(), Resolved{Location: direct, Descriptor: root}, platforms.All)
	require.NoError(t, err)
	require.Equal(t, root, img.Descriptor)
	require.NoError(t, img.Close())

	other := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageIndex, Digest: digest.FromString("other")}
	_, err = accessor.Load(t.Context(), Resolved{Location: direct, Descriptor: other}, platforms.All)
	require.ErrorContains(t, err, "tarball does not contain")
}

func TestHTTPAccessorResolveRefused(t *testing.T) {
	srv, dgst, _ := serveTestTarball(t)
	host := srv.Listener.Addr().String()

	tests := []struct {
		name    string
		path    string
		header  http.Header
		wantErr string
	}{
		{
			name:    "digest-denied",
			path:    "/ns/denied/@" + dgst.String(),
			wantErr: "403",
		},
		{
			name:    "digest-without-header",
			path:    "/ns/img/@" + dgst.String(),
			wantErr: "403",
		},
		{
			name:    "soft-404",
			path:    "/ns/soft404/latest",
			header:  http.Header{"X-Test": {"yes"}},
			wantErr: "unexpected content type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loc, err := ParseLocation("http+oci://" + host + tt.path)
			require.NoError(t, err)

			_, err = NewAccessor(WithRegistryHeaders(tt.header)).Resolve(t.Context(), loc)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
