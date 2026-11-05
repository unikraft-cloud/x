// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

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
	for _, img := range images {
		var p ocispec.Platform
		if img.Image != nil {
			p = img.Image.Platform
		}
		saved.Platforms = append(saved.Platforms, p)
	}

	// Only a registry needs the accessor, for its resolver and credentials.
	access := imagespec.NewAccessor()
	verb := "save"
	if dest.Scheme != schemes.OCI && slices.ContainsFunc(images, func(img *imagespec.Image) bool { return pushedTag(img) != "" }) {
		return Saved{}, fmt.Errorf("images that the packer pushed can only be saved to a registry, not to %s", dest)
	}
	if dest.Scheme == schemes.OCI {
		verb = "push"
		named, err := reference.ParseNormalizedNamed(dest.Path)
		if err != nil {
			return Saved{}, fmt.Errorf("parsing image reference %q: %w", dest.Path, err)
		}
		saved.Ref = distref.TagNameOnly(named).String()
		if access, err = b.images(ctx); err != nil {
			return Saved{}, err
		}
	}

	var res imagespec.Resolved
	err := progress.Step(progress.WithPrefix(b.progressWriter(), verb), saved.Ref, func() (err error) {
		res, err = access.Save(ctx, dest, images...)
		return err
	})
	if err != nil {
		return Saved{}, err
	}
	saved.Digest = res.Descriptor.Digest

	// The index now holds what the packer pushed under its temporary tags.
	for _, img := range images {
		if tag := pushedTag(img); tag != "" {
			if err := b.untag(ctx, dest, img.ManifestDescriptor.Digest, tag); err != nil {
				log.G(ctx).Warn().Err(err).Str("tag", tag).Msg("leaving the temporary tag of the packer")
			}
		}
	}

	log.G(ctx).Debug().
		Str("ref", saved.Ref).
		Str("digest", saved.Digest.String()).
		Msg("saved images")

	return saved, nil
}

// untag removes tag from the manifest dgst in the repository of dest, through
// the API of Harbor, as the registry API cannot remove only a tag.
func (b Builder) untag(ctx context.Context, dest *imagespec.Location, dgst digest.Digest, tag string) error {
	named, err := reference.ParseNormalizedNamed(dest.Path)
	if err != nil {
		return err
	}
	host := distref.Domain(named)
	project, repo, ok := strings.Cut(distref.Path(named), "/")
	if !ok {
		return fmt.Errorf("%s names no Harbor project", named.Name())
	}
	scheme := "https"
	if local, _ := docker.MatchLocalhost(host); local {
		scheme = "http"
	}
	// Harbor takes a repository with a slash only escaped twice.
	u := fmt.Sprintf("%s://%s/api/v2.0/projects/%s/repositories/%s/artifacts/%s/tags/%s",
		scheme, host, url.PathEscape(project), url.PathEscape(url.PathEscape(repo)), dgst, url.PathEscape(tag))
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("removing tag %s of %s: %s", tag, named.Name(), resp.Status)
	}
	return nil
}
