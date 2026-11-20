// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"context"
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
// user's own origin. The origin may answer a tag with a redirect to a URL
// naming a digest, or serve the tarball under the tag directly.
//
// That digest only addresses the tarball: origins disagree on what it is the
// digest of (the index.json, or the manifest), so it is never checked against
// what they serve. The tarball's own index.json is its root, as for an archive.

// MediaTypeOCILayoutTar is the media type of an OCI image layout served as a
// tarball.
const MediaTypeOCILayoutTar = "application/vnd.oci.layout.v1+tar"

// ResolveTarballRemote resolves the URL of a layout served over HTTP to the URL
// the origin pins it at. An origin that answers a tag with a redirect, under
// the same host, to a URL naming a digest pins it there; one that serves the
// tag directly leaves it unpinned, and u is returned as is. A URL that already
// names a digest is checked with a HEAD and returned as is.
//
// The origin is always asked, so that it can refuse access: knowing a digest
// is not enough to fetch it.
func ResolveTarballRemote(ctx context.Context, u *url.URL, client *http.Client, header http.Header) (*url.URL, error) {
	c := http.Client{}
	if client != nil {
		c = *client
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	method := http.MethodGet
	err := namesDigest(u)
	pinned := err == nil
	if pinned {
		method = http.MethodHead
	}
	rsp, err := requestTarball(ctx, &c, method, u, header)
	if err != nil {
		return nil, fmt.Errorf("resolving %q: %w", u, err)
	}
	defer rsp.Body.Close()

	switch {
	case rsp.StatusCode == http.StatusOK:
		if err := checkLayoutContentType(rsp); err != nil {
			return nil, fmt.Errorf("resolving %q: %w", u, err)
		}
		return u, nil
	case pinned:
		return nil, fmt.Errorf("resolving %q: unexpected status %s", u, rsp.Status)
	}

	switch rsp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return nil, fmt.Errorf("resolving %q: unexpected status %s", u, rsp.Status)
	}

	location := rsp.Header.Get("Location")
	if location == "" {
		return nil, fmt.Errorf("resolving %q: redirect without a location", u)
	}
	target, err := url.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("resolving %q: invalid location %q: %w", u, location, err)
	}
	if target.Host != "" && target.Host != u.Host {
		return nil, fmt.Errorf("resolving %q: redirect to another host %q is not supported", u, target.Host)
	}

	target = &url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.ResolveReference(target).Path}
	if err := namesDigest(target); err != nil {
		return nil, fmt.Errorf("resolving %q: location %q: %w", u, location, err)
	}
	return target, nil
}

// checkLayoutContentType rejects a response that does not claim to be a
// layout, since a soft-404 or catch-all page answers 200 to anyone.
func checkLayoutContentType(rsp *http.Response) error {
	mediaType, _, err := mime.ParseMediaType(rsp.Header.Get("Content-Type"))
	if err != nil {
		return fmt.Errorf("invalid content type: %w", err)
	}
	if mediaType != MediaTypeOCILayoutTar {
		return fmt.Errorf("unexpected content type %q", mediaType)
	}
	return nil
}

// LoadTarballRemote loads the layout tarball at u, from desc if it is set, or
// else from the tarball's own index.json, as LoadTarballReader does. A digest
// in u is not checked against what is served.
func LoadTarballRemote(ctx context.Context, u *url.URL, desc ocispec.Descriptor, client *http.Client, header http.Header, platform platforms.MatchComparer) (*Image, error) {
	body, err := openTarballRemote(ctx, u, client, header)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	img, err := LoadTarballReader(ctx, body, desc, platform)
	if err != nil {
		return nil, err
	}
	img.Resolved = tarballResolved(u, img.Descriptor)
	return img, nil
}

// LoadAllTarballsRemote loads all images from the layout tarball at u, as
// LoadTarballRemote does.
func LoadAllTarballsRemote(ctx context.Context, u *url.URL, desc ocispec.Descriptor, client *http.Client, header http.Header, platform platforms.MatchComparer) ([]*Image, error) {
	body, err := openTarballRemote(ctx, u, client, header)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	imgs, err := LoadAllTarballReaders(ctx, body, desc, platform)
	if err != nil {
		return nil, err
	}
	for _, img := range imgs {
		img.Resolved = tarballResolved(u, img.Descriptor)
	}
	return imgs, nil
}

// namesDigest checks that the final path segment of u names a digest, which is
// how an origin addresses a pinned tarball.
func namesDigest(u *url.URL) error {
	_, identifier := path.Split(u.Path)
	if !strings.HasPrefix(identifier, "@") {
		return fmt.Errorf("%q does not name a digest", u.Path)
	}
	if _, err := digest.Parse(identifier[1:]); err != nil {
		return fmt.Errorf("%q does not name a digest: %w", u.Path, err)
	}
	return nil
}

// tarballResolved is as much of a Resolved as the URL of a layout and the root
// loaded from it give: the Accessor keeps the one it was handed instead.
func tarballResolved(u *url.URL, root ocispec.Descriptor) Resolved {
	scheme := schemes.Scheme(u.Scheme + "+oci")
	res := Resolved{Location: Location{Scheme: scheme, Path: u.Host + u.Path}}
	if ref, err := reference.Parse(res.Location.String()); err == nil {
		res.Reference = ref
		if ref.Digest() != "" {
			res.ResolvedReference = ref
		}
	}
	res.Descriptor = root
	return res
}

func openTarballRemote(ctx context.Context, u *url.URL, client *http.Client, header http.Header) (io.ReadCloser, error) {
	rsp, err := requestTarball(ctx, client, http.MethodGet, u, header)
	if err != nil {
		return nil, fmt.Errorf("fetching %q: %w", u, err)
	}
	if rsp.StatusCode != http.StatusOK {
		rsp.Body.Close()
		return nil, fmt.Errorf("fetching %q: unexpected status %s", u, rsp.Status)
	}

	decoded, err := decodeContent(rsp.Header.Get("Content-Encoding"), rsp.Body)
	if err != nil {
		rsp.Body.Close()
		return nil, fmt.Errorf("fetching %q: %w", u, err)
	}

	return struct {
		io.Reader
		io.Closer
	}{decoded, xio.Closers{decoded, rsp.Body}}, nil
}

// acceptEncoding lists the content encodings decodeContent undoes.
var acceptEncoding = []string{"zstd", "gzip", "deflate", "identity"}

// requestTarball asks for the layout tarball at u.
func requestTarball(ctx context.Context, client *http.Client, method string, u *url.URL, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if header != nil {
		req.Header = header.Clone()
	}
	req.Header.Set("Accept", MediaTypeOCILayoutTar)
	req.Header.Set("Accept-Encoding", strings.Join(acceptEncoding, ", "))

	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
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
