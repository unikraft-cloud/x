// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/containerd/platforms"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zlib"
	"github.com/klauspost/compress/zstd"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"unikraft.com/x/image-spec/reference"
	"unikraft.com/x/image-spec/schemes"
	xio "unikraft.com/x/io"
)

// This file provides functions for loading images from an OCI image layout
// tarball served over HTTP, which is how the platform fetches an image from a
// user's own origin.
//
// The URL is only an address: no part of it is read as a tag or a digest, and
// the origin may redirect it, under the same host, to wherever it serves the
// tarball. What is served is read as an archive with no tag. The one thing its
// shape decides is that a URL whose last segment names a digest is resolved
// with a HEAD.

// MediaTypeOCILayoutTar is the media type of an OCI image layout served as a
// tarball.
const MediaTypeOCILayoutTar = "application/vnd.oci.layout.v1+tar"

// layoutContentTypes are the content types the agent takes for a layout.
var layoutContentTypes = []string{
	MediaTypeOCILayoutTar,
	"application/vnd.oci.layout.v1.tar",
	"application/x-tar",
	"application/x-gtar",
	"application/tar",
}

const maxTarballRedirects = 10

// ResolveTarballRemote resolves the URL of a layout served over HTTP to the URL
// the origin serves it at, following its redirects as loading it does.
//
// The origin is always asked, so that it can refuse access: with a HEAD for a
// URL whose last segment names a digest, as an origin addresses a tarball that
// does not change, and with the GET that loads the layout for any other.
func ResolveTarballRemote(ctx context.Context, u *url.URL, client *http.Client, header http.Header) (*url.URL, error) {
	rsp, err := followTarball(ctx, client, u, header, func(u *url.URL) string {
		if namesDigest(u) {
			return http.MethodHead
		}
		return http.MethodGet
	})
	if err != nil {
		return nil, reference.StripQueryError(fmt.Errorf("resolving %q: %w", reference.StripQuery(u.String()), err))
	}
	// HACK: for a GET, this starts a download only to drop it, which is suboptimal.
	defer rsp.Body.Close()

	switch {
	case rsp.StatusCode == http.StatusOK:
	case rsp.Request.Method == http.MethodHead:
		return nil, fmt.Errorf("resolving %q: the origin refused a HEAD of %q with status %s", reference.StripQuery(u.String()), reference.StripQuery(rsp.Request.URL.String()), rsp.Status)
	default:
		return nil, fmt.Errorf("resolving %q: unexpected status %s", reference.StripQuery(u.String()), rsp.Status)
	}
	if err := checkLayoutContentType(rsp); err != nil {
		return nil, fmt.Errorf("resolving %q: %w", reference.StripQuery(u.String()), err)
	}
	return rsp.Request.URL, nil
}

// namesDigest reports whether the last segment of the path of u is '@' and a
// digest.
func namesDigest(u *url.URL) bool {
	_, last := path.Split(u.Path)
	identifier, ok := strings.CutPrefix(last, "@")
	if !ok {
		return false
	}
	_, err := digest.Parse(identifier)
	return err == nil
}

// checkLayoutContentType rejects a response that does not claim to be a
// layout, since a soft-404 or catch-all page answers 200 to anyone.
func checkLayoutContentType(rsp *http.Response) error {
	mediaType, _, err := mime.ParseMediaType(rsp.Header.Get("Content-Type"))
	if err != nil {
		return fmt.Errorf("invalid content type: %w", err)
	}
	if !slices.Contains(layoutContentTypes, mediaType) {
		return fmt.Errorf("unexpected content type %q", mediaType)
	}
	return nil
}

// LoadTarballRemote loads the layout tarball at u, from desc if it is set, or
// else as LoadTarballReader does with no tag: no part of u is one.
func LoadTarballRemote(ctx context.Context, u *url.URL, desc ocispec.Descriptor, client *http.Client, header http.Header, platform platforms.MatchComparer) (*Image, error) {
	body, err := openTarballRemote(ctx, u, client, header)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	img, err := LoadTarballReader(ctx, body, desc, platform)
	if err != nil {
		return nil, fmt.Errorf("tarball %q: %w", reference.StripQuery(u.String()), err)
	}
	img.Resolved = tarballResolved(u, img.Descriptor)
	return img, nil
}

// LoadAllTarballsRemote loads the images of every platform from the layout
// tarball at u, under the one root that LoadTarballRemote loads from, and not
// every image the tarball lists. The images share one temporary store, which
// stays until all of them are closed.
func LoadAllTarballsRemote(ctx context.Context, u *url.URL, desc ocispec.Descriptor, client *http.Client, header http.Header, platform platforms.MatchComparer) ([]*Image, error) {
	body, err := openTarballRemote(ctx, u, client, header)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	imgs, err := LoadAllTarballReaders(ctx, body, desc, platform)
	if err != nil {
		return nil, fmt.Errorf("tarball %q: %w", reference.StripQuery(u.String()), err)
	}
	for _, img := range imgs {
		img.Resolved = tarballResolved(u, img.Descriptor)
	}
	return imgs, nil
}

// tarballResolved is as much of a Resolved as the URL of a layout and the root
// loaded from it give: the Accessor keeps the one it was handed instead.
func tarballResolved(u *url.URL, root ocispec.Descriptor) Resolved {
	res := Resolved{Location: tarballLocation(u), Descriptor: root}
	if ref, err := reference.FromURL(u); err == nil {
		res.Reference = ref
	}
	return res
}

func tarballLocation(u *url.URL) Location {
	return Location{
		Scheme: schemes.Scheme(u.Scheme + "+oci"),
		Path:   strings.TrimPrefix(u.String(), u.Scheme+"://"),
	}
}

// tarballURL is the URL that loc was made of by tarballLocation, or by a user.
func tarballURL(loc Location) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSuffix(string(loc.Scheme), "+oci") + "://" + loc.Path)
	if err != nil {
		return nil, reference.StripQueryError(err)
	}
	if _, err := reference.FromURL(u); err != nil {
		return nil, err
	}
	return u, nil
}

func openTarballRemote(ctx context.Context, u *url.URL, client *http.Client, header http.Header) (io.ReadCloser, error) {
	rsp, err := followTarball(ctx, client, u, header, func(*url.URL) string { return http.MethodGet })
	if err != nil {
		return nil, reference.StripQueryError(fmt.Errorf("fetching %q: %w", reference.StripQuery(u.String()), err))
	}
	if rsp.StatusCode != http.StatusOK {
		rsp.Body.Close()
		return nil, fmt.Errorf("fetching %q: unexpected status %s", reference.StripQuery(u.String()), rsp.Status)
	}

	decoded, err := decodeContent(rsp.Header.Get("Content-Encoding"), rsp.Body)
	if err != nil {
		rsp.Body.Close()
		return nil, fmt.Errorf("fetching %q: %w", reference.StripQuery(u.String()), err)
	}

	return struct {
		io.Reader
		io.Closer
	}{decoded, xio.Closers{decoded, rsp.Body}}, nil
}

// followTarball asks for the layout tarball at u, and follows the redirects
// that redirectTarget allows, no more than maxTarballRedirects of them. Each
// URL is asked with the method that method gives for it. The CheckRedirect of
// client is asked before each redirect, and can only refuse it.
func followTarball(ctx context.Context, client *http.Client, u *url.URL, header http.Header, method func(*url.URL) string) (*http.Response, error) {
	var via []*http.Request
	for {
		rsp, err := requestTarball(ctx, client, method(u), u, header)
		if err != nil {
			return nil, err
		}
		next, err := redirectTarget(rsp)
		if next == nil && err == nil {
			return rsp, nil
		}
		rsp.Body.Close()
		if err != nil {
			return nil, err
		}

		via = append(via, rsp.Request)
		if len(via) > maxTarballRedirects {
			return nil, fmt.Errorf("more than %d redirects", maxTarballRedirects)
		}
		if client != nil && client.CheckRedirect != nil {
			req, err := http.NewRequestWithContext(ctx, method(next), next.String(), nil)
			if err != nil {
				return nil, reference.StripQueryError(err)
			}
			if err := client.CheckRedirect(req, via); err != nil {
				return nil, fmt.Errorf("redirect to %q: %w", reference.StripQuery(next.String()), err)
			}
		}
		u = next
	}
}

// acceptEncoding lists the content encodings decodeContent undoes.
var acceptEncoding = []string{"zstd", "gzip", "deflate", "identity"}

// requestTarball asks for the layout tarball at u, and returns the answer of
// the origin as it is, a redirect too.
func requestTarball(ctx context.Context, client *http.Client, method string, u *url.URL, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, reference.StripQueryError(err)
	}
	if header != nil {
		req.Header = header.Clone()
	}
	req.Header.Set("Accept", MediaTypeOCILayoutTar)
	req.Header.Set("Accept-Encoding", strings.Join(acceptEncoding, ", "))

	c := *http.DefaultClient
	if client != nil {
		c = *client
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	rsp, err := c.Do(req)
	return rsp, reference.StripQueryError(err)
}

// redirectTarget returns where rsp redirects to, or nil if it does not. A
// redirect stays on the same host, since the headers of the caller go along.
func redirectTarget(rsp *http.Response) (*url.URL, error) {
	switch rsp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return nil, nil
	}

	u := rsp.Request.URL
	location := rsp.Header.Get("Location")
	if location == "" {
		return nil, errors.New("redirect without a location")
	}
	target, err := url.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("redirect to an invalid location: %w", reference.StripQueryError(err))
	}
	next := u.ResolveReference(target)
	next.Fragment, next.RawFragment = "", ""
	if next.Scheme != "http" && next.Scheme != "https" {
		return nil, fmt.Errorf("redirect to a location of scheme %q is not supported", next.Scheme)
	}
	if next.User != nil {
		return nil, errors.New("redirect to a location with credentials is not supported")
	}
	if !sameHost(u, next) {
		return nil, fmt.Errorf("redirect to another host %q is not supported", next.Host)
	}
	// An origin behind a TLS proxy writes http locations: never follow one out of https.
	if u.Scheme == "https" && next.Scheme == "http" {
		next.Scheme, next.Host = u.Scheme, u.Host
	}
	return next, nil
}

// sameHost reports whether a and b name the same host and port, however they
// write them. The scheme may differ, as long as neither names a port that is
// not the default of its own scheme.
func sameHost(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if (u.Scheme == "http" && u.Port() == "80") || (u.Scheme == "https" && u.Port() == "443") {
			return ""
		}
		return u.Port()
	}
	return strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

// decodeContent undoes the content encodings applied to r, last first.
func decodeContent(contentEncoding string, r io.Reader) (io.ReadCloser, error) {
	encodings := strings.Split(contentEncoding, ",")
	slices.Reverse(encodings)

	rc := io.NopCloser(r)
	for _, encoding := range encodings {
		switch strings.TrimSpace(encoding) {
		case "", "identity":
		case "gzip":
			g, err := gzip.NewReader(rc)
			if err != nil {
				return nil, fmt.Errorf("decoding gzip content: %w", err)
			}
			rc = g
		case "zstd":
			z, err := zstd.NewReader(rc)
			if err != nil {
				return nil, fmt.Errorf("decoding zstd content: %w", err)
			}
			rc = z.IOReadCloser()
		case "deflate":
			z, err := zlib.NewReader(rc)
			if err != nil {
				return nil, fmt.Errorf("decoding deflate content: %w", err)
			}
			rc = z
		default:
			return nil, fmt.Errorf("unsupported content encoding %q", strings.TrimSpace(encoding))
		}
	}
	return rc, nil
}
