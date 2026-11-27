// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"bytes"
	"cmp"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
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
	mux.HandleFunc("/ns/elsewhere/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://other.example.com/ns/img/latest", http.StatusFound)
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
	}{
		{
			name:         "redirect",
			path:         "/ns/redirect/latest",
			wantLocation: host + "/ns/img/@" + dgst.String(),
		},
		{
			name:         "redirect-relative",
			path:         "/ns/img/relative",
			wantLocation: host + "/ns/img/@" + dgst.String(),
		},
		{
			name:         "redirect-parent",
			path:         "/ns/other/parent",
			wantLocation: host + "/ns/img/@" + dgst.String(),
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
			require.Equal(t, "http+oci://"+tt.wantLocation, res.ResolvedReference.String())
			require.Empty(t, res.ResolvedReference.Digest())
			require.Equal(t, loc.String(), res.Reference.String())
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

func TestHTTPAccessorURLIsOnlyAnAddress(t *testing.T) {
	serve := func(t *testing.T, tarball []byte) func(path string) Location {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", MediaTypeOCILayoutTar)
			_, _ = w.Write(tarball)
		}))
		t.Cleanup(srv.Close)
		return func(path string) Location {
			return Location{Scheme: schemes.HTTPOCI, Path: srv.Listener.Addr().String() + path}
		}
	}
	paths := []string{"/ns/app/v2", "/ns/app/@" + digest.FromString("other").String(), "/app.tar"}
	accessor := NewAccessor()

	for _, tag := range []string{"", "v1"} {
		t.Run("one image tagged "+strconv.Quote(tag), func(t *testing.T) {
			var tarball bytes.Buffer
			root, err := SaveTarballWriterNamed(t.Context(), &tarball, tag, testImage)
			require.NoError(t, err)
			at := serve(t, tarball.Bytes())

			for _, path := range paths {
				loc := at(path)
				res, err := accessor.Resolve(t.Context(), &loc)
				require.NoError(t, err)
				img, err := accessor.Load(t.Context(), res, platforms.All)
				require.NoError(t, err, path)
				require.Equal(t, root, img.Descriptor, path)
				require.NoError(t, img.Close())
			}
		})
	}

	t.Run("several images without latest", func(t *testing.T) {
		layout := t.TempDir()
		for _, tag := range []string{"v1", "v2"} {
			_, err := SaveOCILayoutNamed(t.Context(), layout, tag, platformImages(ocispec.Platform{OS: tag, Architecture: "x86_64"})...)
			require.NoError(t, err)
		}
		at := serve(t, tarLayout(t, layout))

		for _, path := range paths {
			_, err := accessor.Load(t.Context(), Resolved{Location: at(path)}, platforms.All)
			require.ErrorContains(t, err, "give a tag", path)
		}
	})
}

func TestHTTPAccessorResolvesToTheEnd(t *testing.T) {
	var tarball bytes.Buffer
	root, err := SaveTarballWriter(t.Context(), &tarball, testImage)
	require.NoError(t, err)

	var asked []string
	hops := map[string]string{"/ns/app/latest": "/ns/app/v3", "/ns/app/v3": "../blobs/layout.tar?token=a%2Fb"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Method+" "+r.RequestURI)
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if next, ok := hops[r.URL.Path]; ok {
			w.Header().Set("Location", next)
			w.WriteHeader(http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/x-tar")
		_, _ = w.Write(tarball.Bytes())
	}))
	t.Cleanup(srv.Close)
	host := srv.Listener.Addr().String()
	at := func(path string) *Location {
		return &Location{Scheme: schemes.HTTPOCI, Path: host + path}
	}

	accessor := NewAccessor()
	res, err := accessor.Resolve(t.Context(), at("/ns/app/latest"))
	require.NoError(t, err)
	require.Equal(t, []string{"GET /ns/app/latest", "GET /ns/app/v3", "GET /ns/blobs/layout.tar?token=a%2Fb"}, asked)
	require.Equal(t, *at("/ns/blobs/layout.tar?token=a%2Fb"), res.Location)
	require.Equal(t, "http+oci://"+host+"/ns/app/latest", res.Reference.String())
	require.Equal(t, "http+oci://"+host+"/ns/blobs/layout.tar?token=a%2Fb", res.ResolvedReference.String())

	asked = nil
	img, err := accessor.Load(t.Context(), res, platforms.All)
	require.NoError(t, err)
	require.Equal(t, []string{"GET /ns/blobs/layout.tar?token=a%2Fb"}, asked)
	require.Equal(t, root, img.Descriptor)
	require.NoError(t, img.Close())

	asked = nil
	unresolved := Resolved{Location: *at("/ns/app/latest")}
	img, err = accessor.Load(t.Context(), unresolved, platforms.All)
	require.NoError(t, err)
	require.Equal(t, []string{"GET /ns/app/latest", "GET /ns/app/v3", "GET /ns/blobs/layout.tar?token=a%2Fb"}, asked)
	require.NoError(t, img.Close())

	refusing := NewAccessor(WithHTTPClient(&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("no redirects here")
	}}))
	_, err = refusing.Resolve(t.Context(), at("/ns/app/latest"))
	require.ErrorContains(t, err, "no redirects here")
	_, err = refusing.Load(t.Context(), unresolved, platforms.All)
	require.ErrorContains(t, err, "no redirects here")
}

func TestHTTPAccessorResolvesDigestWithHead(t *testing.T) {
	var tarball bytes.Buffer
	root, err := SaveTarballWriter(t.Context(), &tarball, testImage)
	require.NoError(t, err)
	named := "@" + digest.FromString("anything").String()
	encoded := strings.Replace(named, ":", "%3A", 1)

	var asked []string
	refuseHead := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Method+" "+r.RequestURI)
		switch {
		case r.Method == http.MethodHead && refuseHead:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case r.URL.Path == "/ns/app/latest":
			http.Redirect(w, r, "/ns/app/"+named, http.StatusFound)
		case r.URL.Path == "/ns/app/slash":
			http.Redirect(w, r, "/ns/app/"+named+"/", http.StatusFound)
		case r.URL.Path == "/ns/app/encoded":
			w.Header().Set("Location", "/ns/app/"+encoded)
			w.WriteHeader(http.StatusFound)
		default:
			w.Header().Set("Content-Type", MediaTypeOCILayoutTar)
			_, _ = w.Write(tarball.Bytes())
		}
	}))
	t.Cleanup(srv.Close)
	host := srv.Listener.Addr().String()
	at := func(path string) *Location {
		return &Location{Scheme: schemes.HTTPOCI, Path: host + path}
	}
	accessor := NewAccessor()

	for _, tc := range []struct {
		name  string
		path  string
		asked []string
		want  string
	}{
		{name: "a digest", path: "/ns/app/" + named, asked: []string{"HEAD /ns/app/" + named}, want: "/ns/app/" + named},
		{name: "an encoded digest", path: "/ns/app/" + encoded, asked: []string{"HEAD /ns/app/" + encoded}, want: "/ns/app/" + encoded},
		{name: "a redirect to a digest", path: "/ns/app/latest", asked: []string{"GET /ns/app/latest", "HEAD /ns/app/" + named}, want: "/ns/app/" + named},
		{name: "a redirect to an encoded digest", path: "/ns/app/encoded", asked: []string{"GET /ns/app/encoded", "HEAD /ns/app/" + encoded}, want: "/ns/app/" + encoded},
		{name: "not a digest", path: "/ns/app/@sha256:nothex", asked: []string{"GET /ns/app/@sha256:nothex"}, want: "/ns/app/@sha256:nothex"},
		{name: "a redirect to a digest and a slash", path: "/ns/app/slash", asked: []string{"GET /ns/app/slash", "GET /ns/app/" + named + "/"}, want: "/ns/app/" + named + "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked = nil
			res, err := accessor.Resolve(t.Context(), at(tc.path))
			require.NoError(t, err)
			require.Equal(t, tc.asked, asked)
			require.Equal(t, *at(tc.want), res.Location)
			require.Empty(t, res.ResolvedReference.Digest())

			asked = nil
			img, err := accessor.Load(t.Context(), res, platforms.All)
			require.NoError(t, err)
			require.Equal(t, []string{"GET " + tc.want}, asked)
			require.Equal(t, root, img.Descriptor)
			require.NoError(t, img.Close())
		})
	}

	t.Run("an origin that refuses a HEAD", func(t *testing.T) {
		refuseHead = true
		_, err := accessor.Resolve(t.Context(), at("/ns/app/"+named))
		require.ErrorContains(t, err, "refused a HEAD")
		require.ErrorContains(t, err, "405")
		_, err = accessor.Resolve(t.Context(), at("/ns/app/latest"))
		require.ErrorContains(t, err, "refused a HEAD")

		res, err := accessor.Resolve(t.Context(), at("/ns/app/v1"))
		require.NoError(t, err, "a URL that names no digest is never sent a HEAD")
		require.Equal(t, *at("/ns/app/v1"), res.Location)
	})
}

func TestHTTPAccessorRedirectCap(t *testing.T) {
	var tarball bytes.Buffer
	_, err := SaveTarballWriter(t.Context(), &tarball, testImage)
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		left, err := strconv.Atoi(path.Base(r.URL.Path))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if left > 0 {
			http.Redirect(w, r, "/ns/hops/"+strconv.Itoa(left-1), http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", MediaTypeOCILayoutTar)
		_, _ = w.Write(tarball.Bytes())
	}))
	t.Cleanup(srv.Close)
	at := func(hops int) Resolved {
		return Resolved{Location: Location{Scheme: schemes.HTTPOCI, Path: srv.Listener.Addr().String() + "/ns/hops/" + strconv.Itoa(hops)}}
	}

	accessor := NewAccessor()
	img, err := accessor.Load(t.Context(), at(maxTarballRedirects), platforms.All)
	require.NoError(t, err)
	require.NoError(t, img.Close())

	_, err = accessor.Load(t.Context(), at(maxTarballRedirects+1), platforms.All)
	require.ErrorContains(t, err, "more than 10 redirects")

	over := at(maxTarballRedirects + 1).Location
	_, err = accessor.Resolve(t.Context(), &over)
	require.ErrorContains(t, err, "more than 10 redirects")
	last := at(maxTarballRedirects).Location
	res, err := accessor.Resolve(t.Context(), &last)
	require.NoError(t, err)
	require.Equal(t, at(0).Location, res.Location)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestHTTPAccessorKeepsHTTPS(t *testing.T) {
	var tarball bytes.Buffer
	root, err := SaveTarballWriter(t.Context(), &tarball, testImage)
	require.NoError(t, err)

	type request struct {
		url, authorization string
	}
	var asked []request
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		asked = append(asked, request{req.URL.String(), req.Header.Get("Authorization")})
		rsp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody, Request: req}
		if req.URL.Path == "/ns/app/latest" {
			rsp.StatusCode = http.StatusFound
			rsp.Header.Set("Location", "http://origin.test/blobs/app.tar?sig=a%2Fb")
			return rsp, nil
		}
		rsp.Header.Set("Content-Type", MediaTypeOCILayoutTar)
		rsp.Body = io.NopCloser(bytes.NewReader(tarball.Bytes()))
		return rsp, nil
	})}
	accessor := NewAccessor(WithHTTPClient(client), WithRegistryHeaders(http.Header{"Authorization": {"Bearer secret"}}))
	latest := Location{Scheme: schemes.HTTPSOCI, Path: "origin.test/ns/app/latest"}

	res, err := accessor.Resolve(t.Context(), &latest)
	require.NoError(t, err)
	require.Equal(t, Location{Scheme: schemes.HTTPSOCI, Path: "origin.test/blobs/app.tar?sig=a%2Fb"}, res.Location)
	require.Equal(t, []request{
		{"https://origin.test/ns/app/latest", "Bearer secret"},
		{"https://origin.test/blobs/app.tar?sig=a%2Fb", "Bearer secret"},
	}, asked)

	asked = nil
	img, err := accessor.Load(t.Context(), Resolved{Location: latest}, platforms.All)
	require.NoError(t, err)
	require.Equal(t, []request{
		{"https://origin.test/ns/app/latest", "Bearer secret"},
		{"https://origin.test/blobs/app.tar?sig=a%2Fb", "Bearer secret"},
	}, asked, "the headers never go out over http")
	require.Equal(t, root, img.Descriptor)
	require.NoError(t, img.Close())
}

func TestHTTPAccessorSameHost(t *testing.T) {
	for _, tc := range []struct {
		from     schemes.Scheme
		location string
		want     string
		wantErr  string
	}{
		{location: "https://origin.test/b", want: "https+oci://origin.test/b"},
		{location: "https://origin.test:443/b", want: "https+oci://origin.test:443/b"},
		{location: "https://ORIGIN.test/b", want: "https+oci://ORIGIN.test/b"},
		{location: "http://origin.test/b", want: "https+oci://origin.test/b"},
		{location: "http://origin.test:80/b", want: "https+oci://origin.test/b"},
		{from: schemes.HTTPOCI, location: "https://origin.test/b", want: "https+oci://origin.test/b"},
		{from: schemes.HTTPOCI, location: "http://origin.test/b", want: "http+oci://origin.test/b"},
		{location: "https://origin.test:8443/b", wantErr: "redirect to another host"},
		{location: "http://origin.test:443/b", wantErr: "redirect to another host"},
		{location: "https://other.test/b", wantErr: "redirect to another host"},
		{location: "https://origin.test.other.test/b", wantErr: "redirect to another host"},
		{location: "https://user:pw@origin.test/b", wantErr: "credentials"},
	} {
		from := cmp.Or(tc.from, schemes.HTTPSOCI)
		t.Run(string(from)+" to "+tc.location, func(t *testing.T) {
			asked := 0
			client := &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					asked++
					rsp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody, Request: req}
					if req.URL.Path == "/a" {
						rsp.StatusCode = http.StatusFound
						rsp.Header.Set("Location", tc.location)
						return rsp, nil
					}
					rsp.Header.Set("Content-Type", MediaTypeOCILayoutTar)
					return rsp, nil
				}),
				CheckRedirect: func(*http.Request, []*http.Request) error { return nil },
			}

			res, err := NewAccessor(WithHTTPClient(client)).Resolve(t.Context(), &Location{Scheme: from, Path: "origin.test/a"})
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.Equal(t, 1, asked)
				return
			}
			require.NoError(t, err)
			require.Equal(t, 2, asked)
			require.Equal(t, tc.want, res.Location.String())
		})
	}
}

func TestHTTPAccessorRefusesRedirect(t *testing.T) {
	for _, tc := range []struct {
		name     string
		location string
		wantErr  string
	}{
		{name: "credentials", location: "https://user:hunter2@origin.test/b", wantErr: "credentials"},
		{name: "another scheme", location: "ftp://origin.test/b?sig=hunter2", wantErr: `scheme "ftp"`},
		{name: "a location that does not parse", location: "https://origin.test/b%zz?sig=hunter2", wantErr: "Location"},
		{name: "a port that does not parse", location: "https://origin.test:port/b?sig=hunter2", wantErr: "Location"},
		{name: "a relative location that does not parse", location: "/b%zz?sig=hunter2", wantErr: "Location"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked := 0
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				asked++
				rsp := &http.Response{StatusCode: http.StatusFound, Header: http.Header{}, Body: http.NoBody, Request: req}
				rsp.Header.Set("Location", tc.location)
				return rsp, nil
			})}
			accessor := NewAccessor(WithHTTPClient(client), WithRegistryHeaders(http.Header{"Authorization": {"Bearer secret"}}))
			loc := Location{Scheme: schemes.HTTPSOCI, Path: "origin.test/a?sig=hunter2"}

			_, resolveErr := accessor.Resolve(t.Context(), &loc)
			_, loadErr := accessor.Load(t.Context(), Resolved{Location: loc}, platforms.All)
			_, loadAllErr := accessor.LoadAll(t.Context(), Resolved{Location: loc}, platforms.All)
			for _, err := range []error{resolveErr, loadErr, loadAllErr} {
				require.ErrorContains(t, err, tc.wantErr)
				require.NotContains(t, err.Error(), "hunter2")
				require.NotContains(t, err.Error(), "?")
			}
			require.Equal(t, 3, asked, "the location is never asked")
		})
	}
}

func TestHTTPAccessorStaysOnHost(t *testing.T) {
	var asked atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		asked.Add(1)
	}))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/ns/app/latest", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	loc := Location{Scheme: schemes.HTTPOCI, Path: srv.Listener.Addr().String() + "/ns/app/latest"}

	for name, client := range map[string]*http.Client{
		"default":    nil,
		"permissive": {CheckRedirect: func(*http.Request, []*http.Request) error { return nil }},
	} {
		t.Run(name, func(t *testing.T) {
			accessor := NewAccessor(WithHTTPClient(client), WithRegistryHeaders(http.Header{"Authorization": {"Bearer secret"}}))

			_, err := accessor.Resolve(t.Context(), &loc)
			require.ErrorContains(t, err, "redirect to another host")
			_, err = accessor.Load(t.Context(), Resolved{Location: loc}, platforms.All)
			require.ErrorContains(t, err, "redirect to another host")
			_, err = accessor.LoadAll(t.Context(), Resolved{Location: loc}, platforms.All)
			require.ErrorContains(t, err, "redirect to another host")
			require.Zero(t, asked.Load(), "the other host is never asked")
		})
	}
}

func TestHTTPAccessorKeepsURLAsWritten(t *testing.T) {
	var tarball bytes.Buffer
	root, err := SaveTarballWriter(t.Context(), &tarball, testImage)
	require.NoError(t, err)

	var asked []string
	var redirect string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.RequestURI)
		if r.URL.Path == "/ns/app/latest" {
			w.Header().Set("Location", redirect)
			w.WriteHeader(http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", MediaTypeOCILayoutTar)
		_, _ = w.Write(tarball.Bytes())
	}))
	t.Cleanup(srv.Close)
	host := srv.Listener.Addr().String()
	accessor := NewAccessor()

	for _, served := range []string{
		"/ns/app/@sha256%3Aabc",
		"/t/app(1)!.tar",
		"/t/a%20b%2Fc.tar",
		"/keys//app/",
		"/ns/app/v1?",
		"/ns/app/v1?sig=a%2Fb",
	} {
		t.Run("redirected to "+served, func(t *testing.T) {
			redirect = served
			res, err := accessor.Resolve(t.Context(), &Location{Scheme: schemes.HTTPOCI, Path: host + "/ns/app/latest"})
			require.NoError(t, err)
			require.Equal(t, host+served, res.Location.Path)
			require.Equal(t, "http+oci://"+host+served, res.ResolvedReference.String())

			asked = nil
			img, err := accessor.Load(t.Context(), res, platforms.All)
			require.NoError(t, err)
			require.Equal(t, []string{served}, asked)
			require.Equal(t, root, img.Descriptor)
			require.NoError(t, img.Close())
		})
	}

	for _, written := range []string{"/ns/app/@sha256%3Aabc", "/t/app(1)!.tar", "/t/a%20b%2Fc.tar"} {
		t.Run("asked for "+written, func(t *testing.T) {
			loc, err := ParseLocation("http+oci://" + host + written)
			require.NoError(t, err)

			asked = nil
			res, err := accessor.Resolve(t.Context(), loc)
			require.NoError(t, err)
			require.Equal(t, host+written, res.Location.Path)
			img, err := accessor.Load(t.Context(), res, platforms.All)
			require.NoError(t, err)
			require.Equal(t, []string{written, written}, asked)
			require.NoError(t, img.Close())
		})
	}
}

func TestHTTPAccessorStripsQuery(t *testing.T) {
	down := errors.New("origin is down")
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/ns/app/down":
			return nil, down
		case "/ns/app/moved":
			rsp := &http.Response{StatusCode: http.StatusFound, Header: http.Header{}, Body: http.NoBody, Request: req}
			rsp.Header.Set("Location", "https://other.test/ns/app/latest?sig=secret")
			return rsp, nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Header: http.Header{}, Body: http.NoBody, Request: req}, nil
	})}
	accessor := NewAccessor(WithHTTPClient(client))

	for _, tc := range []struct {
		path string
		is   error
	}{
		{path: "/ns/app/down", is: down},
		{path: "/ns/app/moved"},
		{path: "/ns/app/missing"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			loc := Location{Scheme: schemes.HTTPSOCI, Path: "origin.test" + tc.path + "?sig=secret"}

			_, resolveErr := accessor.Resolve(t.Context(), &loc)
			_, loadErr := accessor.Load(t.Context(), Resolved{Location: loc}, platforms.All)
			for _, err := range []error{resolveErr, loadErr} {
				require.ErrorContains(t, err, "origin.test"+tc.path)
				require.NotContains(t, err.Error(), "secret")
				if tc.is != nil {
					require.ErrorIs(t, err, tc.is)
				}
			}
		})
	}
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
			name:    "another-host",
			path:    "/ns/elsewhere/latest",
			wantErr: `redirect to another host "other.example.com"`,
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
