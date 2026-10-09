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
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/containerd/platforms"
	"github.com/klauspost/compress/flate"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
	"github.com/opencontainers/go-digest"

	"unikraft.com/x/image-spec/reference"
	"unikraft.com/x/image-spec/schemes"
)

// This file provides functions for loading images from an OCI image layout
// tarball served over HTTP, which is how the platform fetches an image from a
// user's own origin. The origin answers a tag with a redirect to the digest it
// currently names; the digest URL serves the tarball itself.

// MediaTypeOCILayoutTar is the media type of an OCI image layout served as a
// tarball.
const MediaTypeOCILayoutTar = "application/vnd.oci.layout.v1+tar"

// ResolveTarballRemote resolves the URL of a layout served over HTTP to the
// URL of the digest it currently names. The origin answers a tag with a
// redirect, under the same host, to that digest. A URL that already names a
// digest is returned without a request.
func ResolveTarballRemote(ctx context.Context, u *url.URL, client *http.Client) (*url.URL, error) {
	if _, err := tarballDigest(u); err == nil {
		return u, nil
	}

	rsp, err := requestTarball(ctx, client, u)
	if err != nil {
		return nil, fmt.Errorf("resolving %q: %w", u, err)
	}
	defer rsp.Body.Close()

	switch rsp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return nil, fmt.Errorf("resolving %q: expected a redirect, got %s", u, rsp.Status)
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

	pinned := &url.URL{Scheme: u.Scheme, Host: u.Host, Path: target.Path}
	if _, err := tarballDigest(pinned); err != nil {
		return nil, fmt.Errorf("resolving %q: location %q: %w", u, location, err)
	}
	return pinned, nil
}

// LoadTarballRemote loads the layout tarball at u, whose final path segment
// names the digest of the bytes as served, before any content encoding is
// undone. The load fails if they do not match.
func LoadTarballRemote(ctx context.Context, u *url.URL, client *http.Client, platform platforms.MatchComparer) (*Image, error) {
	body, err := openTarballRemote(ctx, u, client)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	img, err := LoadTarballReader(ctx, body, platform)
	if err != nil {
		return nil, err
	}
	if err := body.verify(); err != nil {
		return nil, errors.Join(err, img.Close())
	}
	img.Resolved = tarballResolved(u)
	return img, nil
}

// LoadAllTarballsRemote loads all images from the layout tarball at u,
// checking the bytes as LoadTarballRemote does.
func LoadAllTarballsRemote(ctx context.Context, u *url.URL, client *http.Client, platform platforms.MatchComparer) ([]*Image, error) {
	body, err := openTarballRemote(ctx, u, client)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	imgs, err := LoadAllTarballReaders(ctx, body, platform)
	if err != nil {
		return nil, err
	}
	if err := body.verify(); err != nil {
		for _, img := range imgs {
			err = errors.Join(err, img.Close())
		}
		return nil, err
	}
	for _, img := range imgs {
		img.Resolved = tarballResolved(u)
	}
	return imgs, nil
}

// tarballDigest is the digest the final path segment of u names.
func tarballDigest(u *url.URL) (digest.Digest, error) {
	_, identifier := path.Split(u.Path)
	if !strings.HasPrefix(identifier, "@") {
		return "", fmt.Errorf("%q does not name a digest", u.Path)
	}
	dgst, err := digest.Parse(identifier[1:])
	if err != nil {
		return "", fmt.Errorf("%q does not name a digest: %w", u.Path, err)
	}
	return dgst, nil
}

// tarballResolved is as much of a Resolved as the URL of a layout gives: the
// Accessor keeps the one it was handed instead.
func tarballResolved(u *url.URL) Resolved {
	scheme := schemes.Scheme(u.Scheme + "+oci")
	res := Resolved{Location: Location{Scheme: scheme, Path: u.Host + u.Path}}
	if ref, err := reference.Parse(res.Location.String()); err == nil {
		res.Reference = ref
		if ref.Digest() != "" {
			res.ResolvedReference = ref
		}
	}
	return res
}

// remoteTarball is the body of a tarball response: the decoded bytes to read,
// and the served bytes still to be verified against the digest.
type remoteTarball struct {
	io.ReadCloser
	served   io.Reader
	verifier digest.Verifier
	expected digest.Digest
}

// verify drains the served bytes, since the archive may end before the body
// does, and checks them against the digest.
func (t *remoteTarball) verify() error {
	if _, err := io.Copy(io.Discard, t.served); err != nil {
		return fmt.Errorf("draining response body: %w", err)
	}
	if !t.verifier.Verified() {
		return fmt.Errorf("fetched layout does not match %s", t.expected)
	}
	return nil
}

func openTarballRemote(ctx context.Context, u *url.URL, client *http.Client) (*remoteTarball, error) {
	dgst, err := tarballDigest(u)
	if err != nil {
		return nil, err
	}

	rsp, err := requestTarball(ctx, client, u)
	if err != nil {
		return nil, fmt.Errorf("fetching %q: %w", u, err)
	}
	if rsp.StatusCode != http.StatusOK {
		rsp.Body.Close()
		return nil, fmt.Errorf("fetching %q: unexpected status %s", u, rsp.Status)
	}

	verifier := dgst.Verifier()
	served := io.TeeReader(rsp.Body, verifier)

	decoded, err := decodeContent(rsp.Header.Get("Content-Encoding"), served)
	if err != nil {
		rsp.Body.Close()
		return nil, fmt.Errorf("fetching %q: %w", u, err)
	}

	return &remoteTarball{
		ReadCloser: struct {
			io.Reader
			io.Closer
		}{decoded, closers{decoded, rsp.Body}},
		served:   served,
		verifier: verifier,
		expected: dgst,
	}, nil
}

// acceptEncoding lists the content encodings decodeContent undoes.
var acceptEncoding = []string{"zstd", "gzip", "deflate", "identity"}

// requestTarball asks for the layout tarball at u. Redirects are returned
// rather than followed, since a redirect is how the origin resolves a tag.
func requestTarball(ctx context.Context, client *http.Client, u *url.URL) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", MediaTypeOCILayoutTar)
	req.Header.Set("Accept-Encoding", strings.Join(acceptEncoding, ", "))

	c := http.Client{}
	if client != nil {
		c = *client
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return c.Do(req)
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
			rc = flate.NewReader(rc)
		default:
			return nil, fmt.Errorf("unsupported content encoding %q", strings.TrimSpace(encoding))
		}
	}
	return rc, nil
}

// closers closes each closer in turn and joins their errors.
type closers []io.Closer

func (c closers) Close() error {
	var err error
	for _, closer := range c {
		err = errors.Join(err, closer.Close())
	}
	return err
}
