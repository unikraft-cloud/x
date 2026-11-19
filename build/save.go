// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/containerd/containerd/v2/core/remotes/docker"
	distref "github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	imagespec "unikraft.com/x/image-spec"
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

// Save stores images at dest under one index and reports where they are. A
// registry destination is pushed through the Images accessor, which holds
// the credentials.
func (b Builder) Save(ctx context.Context, dest *imagespec.Location, images ...*imagespec.Image) (Saved, error) {
	saved := Saved{Ref: dest.Path, Platforms: make([]ocispec.Platform, 0, len(images))}
	var pushed []ocispec.Descriptor
	for _, img := range images {
		var p ocispec.Platform
		if img.Image != nil {
			p = img.Image.Platform
		}
		saved.Platforms = append(saved.Platforms, p)
		if pushedTag(img) != "" {
			pushed = append(pushed, img.ManifestDescriptor)
		}
	}

	// Only a registry needs the accessor, for its resolver and credentials.
	access := imagespec.NewAccessor()
	verb := "save"
	var named distref.Named
	if dest.Scheme != schemes.OCI && len(pushed) > 0 {
		return Saved{}, fmt.Errorf("images that the packer pushed can only be saved to a registry, not to %s", dest)
	}
	if dest.Scheme == schemes.OCI {
		// The index holds what the packer pushed under its temporary tags, or
		// nothing will.
		defer b.Untag(ctx, dest, images...)
		if len(pushed) > 0 && len(pushed) < len(images) {
			return Saved{}, fmt.Errorf("images that the packer pushed cannot be saved with other images")
		}
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
		if len(pushed) > 0 {
			_, desc, err := imagespec.SaveRegistryIndex(ctx, named, access.Resolver(), pushed...)
			saved.Digest = desc.Digest
			return err
		}
		res, err := access.Save(ctx, dest, images...)
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

// Untag removes the temporary tags that the packer pushed images under in the
// repository of dest, and keeps their manifests. Save does so for its own
// dest; a caller does for the images that it does not save. A tag that stays
// is only logged.
func (b Builder) Untag(ctx context.Context, dest *imagespec.Location, images ...*imagespec.Image) {
	// The build that pushed may be cancelled by now.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	for _, img := range images {
		tag := pushedTag(img)
		if tag == "" {
			continue
		}
		if err := b.untag(ctx, dest, tag); err != nil {
			log.G(ctx).Warn().Err(err).Str("tag", tag).Msg("leaving the temporary tag of the packer")
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
