// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	remoteerrors "github.com/containerd/containerd/v2/core/remotes/errors"
	distref "github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/image-spec/contentutil"
	"unikraft.com/x/image-spec/reference"
	"unikraft.com/x/image-spec/schemes"
	"unikraft.com/x/log"

	"unikraft.com/x/build/progress"
)

// Saved tells where Save stored images.
type Saved struct {
	Ref       string
	Digest    digest.Digest
	Platforms []ocispec.Platform
}

// Save stores what Build made at dest under one index and reports where it
// is. A registry destination is pushed through the Images accessor, which
// holds the credentials. What the packer pushed is saved only to one of
// BuildOpts.Push, where Save removes its temporary tags.
func (b Builder) Save(ctx context.Context, dest *imagespec.Location, built Built) (Saved, error) {
	if len(built.Pushed) > 0 {
		if dest.Scheme != schemes.OCI {
			return Saved{}, fmt.Errorf("images that the packer pushed can only be saved to a registry, not to %s", dest)
		}
		// The index holds what the packer pushed under its temporary tags, or
		// nothing will.
		defer b.Untag(ctx, dest, built.Pushed...)
		if len(built.Images) > 0 {
			return Saved{}, fmt.Errorf("images that the packer pushed cannot be saved with other images")
		}
	}

	saved := Saved{Ref: dest.Path, Platforms: make([]ocispec.Platform, 0, len(built.Images)+len(built.Pushed))}
	for _, img := range built.Images {
		var p ocispec.Platform
		if img.Image != nil {
			p = img.Image.Platform
		}
		saved.Platforms = append(saved.Platforms, p)
	}
	mfsts := make([]ocispec.Descriptor, 0, len(built.Pushed))
	for _, p := range built.Pushed {
		saved.Platforms = append(saved.Platforms, p.Platform)
		mfsts = append(mfsts, p.Manifest)
	}

	// Only a registry needs the accessor, for its resolver and credentials.
	access := imagespec.NewAccessor()
	verb := "save"
	var named distref.Named
	if dest.Scheme == schemes.OCI {
		verb = "push"
		var err error
		named, err = reference.ParseNormalizedNamed(dest.Path)
		if err != nil {
			return Saved{}, fmt.Errorf("parsing image reference %q: %w", dest.Path, err)
		}
		saved.Ref = distref.TagNameOnly(named).String()
		if access, err = b.images(ctx); err != nil {
			return Saved{}, err
		}
	}

	err := progress.Step(progress.WithPrefix(b.progressWriter(), verb), saved.Ref, func() error {
		if len(mfsts) > 0 {
			desc, err := pushIndex(ctx, access.Resolver(), named, mfsts...)
			saved.Digest = desc.Digest
			return err
		}
		res, err := access.Save(ctx, dest, built.Images...)
		saved.Digest = res.Descriptor.Digest
		return err
	})
	if err != nil {
		return Saved{}, err
	}

	log.G(ctx).Debug().
		Str("ref", saved.Ref).
		Str("digest", saved.Digest.String()).
		Msg("saved images")

	return saved, nil
}

// pushIndex pushes to named an index over mfsts, after it checks that the repository holds each.
func pushIndex(ctx context.Context, remote remotes.Resolver, named distref.Named, mfsts ...ocispec.Descriptor) (ocispec.Descriptor, error) {
	named = distref.TagNameOnly(named)

	for _, mfst := range mfsts {
		ref, err := distref.WithDigest(distref.TrimNamed(named), mfst.Digest)
		if err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("naming manifest %s of image %q: %w", mfst.Digest, named, err)
		}
		if err := findManifest(ctx, remote, ref.String()); err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to find manifest %s of image %q: %w", mfst.Digest, named, err)
		}
	}

	data, err := json.MarshalIndent(ocispec.Index{
		SchemaVersion: 2,
		MediaType:     ocispec.MediaTypeImageIndex,
		Manifests:     mfsts,
	}, "", "  ")
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	desc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageIndex,
		Digest:    digest.FromBytes(data),
		Size:      int64(len(data)),
	}

	pusher, err := remote.Pusher(ctx, named.String())
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to get pusher for image %q: %w", named, err)
	}
	if err := content.WriteBlob(ctx, contentutil.FromPusher(pusher), named.Name(), bytes.NewReader(data), desc); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to push index of image %q: %w", named, err)
	}
	return desc, nil
}

// manifestWait bounds the retries of findManifest: Harbor refuses pulls for seconds after a push.
const manifestWait = 30 * time.Second

// findManifest resolves ref, and tries again with backoff until manifestWait while the registry answers 401.
func findManifest(ctx context.Context, remote remotes.Resolver, ref string) error {
	deadline := time.Now().Add(manifestWait)
	delay := 250 * time.Millisecond
	for {
		_, _, err := remote.Resolve(ctx, ref)
		if err == nil || !unauthorized(err) || time.Now().Add(delay).After(deadline) {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(err, ctx.Err())
		case <-timer.C:
		}
		delay = min(2*delay, 5*time.Second)
	}
}

// unauthorized reports whether err is a 401 of the registry, after the resolver tried a new token.
func unauthorized(err error) bool {
	var status remoteerrors.ErrUnexpectedStatus
	return errors.Is(err, docker.ErrInvalidAuthorization) ||
		(errors.As(err, &status) && status.StatusCode == http.StatusUnauthorized)
}

// Untag removes the temporary tags of pushed in the repository of dest, and
// keeps their manifests. Save does so for its own dest; a caller does
// for what it does not save. A tag that stays is only logged.
func (b Builder) Untag(ctx context.Context, dest *imagespec.Location, pushed ...Pushed) {
	// The build that pushed may be cancelled by now.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	for _, p := range pushed {
		if err := b.untag(ctx, dest, p.Tag); err != nil {
			log.G(ctx).Warn().Err(err).Str("tag", p.Tag).Msg("leaving the temporary tag of the packer")
		}
	}
}

// untag removes tag in the repository of dest: through the API of Harbor,
// whose registry API cannot remove only a tag, or else through the registry API.
func (b Builder) untag(ctx context.Context, dest *imagespec.Location, tag string) error {
	named, err := reference.ParseNormalizedNamed(dest.Path)
	if err != nil {
		return err
	}
	host := distref.Domain(named)
	scheme, hc := "https", http.DefaultClient
	if local, _ := docker.MatchLocalhost(host); local {
		scheme = "http"
	}
	if slices.Contains(b.InsecureRegistries, host) {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		hc = &http.Client{Transport: docker.NewHTTPFallback(t)}
	}

	if project, repo, ok := strings.Cut(distref.Path(named), "/"); ok {
		// Harbor takes a repository with a slash only escaped twice.
		u := fmt.Sprintf("%s://%s/api/v2.0/projects/%s/repositories/%s/artifacts/%[5]s/tags/%[5]s",
			scheme, host, url.PathEscape(project), url.PathEscape(url.PathEscape(repo)), url.PathEscape(tag))
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
		if err != nil {
			return err
		}
		if b.Credentials != nil {
			user, secret, err := b.Credentials(host)
			if err != nil {
				return err
			}
			req.SetBasicAuth(user, secret)
		}
		resp, err := hc.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil
		}
	}

	// Not a Harbor, or the tag is gone. Harbor refuses this call.
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, host, distref.Path(named), tag), nil)
	if err != nil {
		return err
	}
	var auth docker.Authorizer
	if b.Credentials != nil {
		auth = docker.NewDockerAuthorizer(docker.WithAuthCreds(b.Credentials), docker.WithAuthClient(hc))
	}
	var resp *http.Response
	for range 2 { // the second with what the registry asked for
		if auth != nil {
			if err := auth.Authorize(ctx, req); err != nil {
				return err
			}
		}
		if resp, err = hc.Do(req); err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized || auth == nil {
			break
		}
		if err := auth.AddResponses(ctx, []*http.Response{resp}); err != nil {
			return err
		}
	}
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("removing tag %s of %s: %s", tag, named.Name(), resp.Status)
	}
	return nil
}
