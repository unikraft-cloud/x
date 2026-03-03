// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"

	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	ctrdreference "github.com/containerd/containerd/v2/pkg/reference"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	"github.com/distribution/reference"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// LoadDockerImage loads a Docker image from a remote registry.
func LoadDockerImage(ctx context.Context, named reference.Named, remote remotes.Resolver, platform platforms.MatchComparer) (*Image, error) {
	named = reference.TagNameOnly(named)

	name, desc, err := remote.Resolve(ctx, named.String())
	if err != nil {
		return nil, fmt.Errorf("failed to resolve image %q: %w", named, err)
	}
	ref, err := reference.Parse(name)
	if err != nil {
		return nil, fmt.Errorf("failed to parse resolved image name %q: %w", name, err)
	}
	named, ok := ref.(reference.Named)
	if !ok {
		return nil, fmt.Errorf("resolved image name %q is not a named reference", name)
	}

	fetcher, err := remote.Fetcher(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to get fetcher for image %q: %w", named, err)
	}
	provider := contentutil.FromFetcher(fetcher)

	img, err := LoadContent(ctx, provider, desc, platform)
	if err != nil {
		return nil, fmt.Errorf("failed to load image %q: %w", named, err)
	}
	img.Name = named
	return img, nil
}

// LoadAllDockerImages loads all available Docker images from a remote registry.
func LoadAllDockerImages(ctx context.Context, named reference.Named, remote remotes.Resolver, platform platforms.MatchComparer) ([]*Image, error) {
	named = reference.TagNameOnly(named)

	name, desc, err := remote.Resolve(ctx, named.String())
	if err != nil {
		return nil, fmt.Errorf("failed to resolve image %q: %w", named, err)
	}
	ref, err := reference.Parse(name)
	if err != nil {
		return nil, fmt.Errorf("failed to parse resolved image name %q: %w", name, err)
	}
	named, ok := ref.(reference.Named)
	if !ok {
		return nil, fmt.Errorf("resolved image name %q is not a named reference", name)
	}

	fetcher, err := remote.Fetcher(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to get fetcher for image %q: %w", named, err)
	}
	provider := contentutil.FromFetcher(fetcher)

	imgs, err := LoadAllContent(ctx, provider, desc, platform)
	if err != nil {
		return nil, fmt.Errorf("failed to load image %q: %w", named, err)
	}
	for _, img := range imgs {
		img.Name = named
	}
	return imgs, nil
}

// SaveDockerImage saves a Docker image to a remote registry.
func SaveDockerImage(ctx context.Context, named reference.Named, remote remotes.Resolver, image ...*Image) (reference.Named, ocispec.Descriptor, error) {
	named = reference.TagNameOnly(named)

	pusher, err := remote.Pusher(ctx, named.String())
	if err != nil {
		return nil, ocispec.Descriptor{}, fmt.Errorf("failed to get pusher for image %q: %w", named, err)
	}
	ingester := contentutil.FromPusher(pusher)

	desc, err := SaveContent(ctx, ingester, named.Name(), image...)
	if err != nil {
		return nil, ocispec.Descriptor{}, fmt.Errorf("failed to save image %q: %w", named, err)
	}
	return named, desc, nil
}

// DeleteDockerImage deletes a Docker image from a remote registry.
func DeleteDockerImage(ctx context.Context, named reference.Named, remote remotes.Resolver, hosts docker.RegistryHosts, headers http.Header) error {
	named = reference.TagNameOnly(named)

	name, desc, err := remote.Resolve(ctx, named.String())
	if err != nil {
		return fmt.Errorf("failed to resolve image %q: %w", named, err)
	}
	if desc.Digest == "" {
		return fmt.Errorf("resolved image %q without digest", named)
	}
	if err := desc.Digest.Validate(); err != nil {
		return fmt.Errorf("resolved image %q with invalid digest: %w", named, err)
	}
	refspec, err := ctrdreference.Parse(name)
	if err != nil {
		return fmt.Errorf("failed to parse resolved image reference %q: %w", name, err)
	}
	ctx, err = docker.ContextWithRepositoryScope(ctx, refspec, true)
	if err != nil {
		return err
	}

	ref, err := reference.Parse(name)
	if err != nil {
		return fmt.Errorf("failed to parse resolved image name %q: %w", name, err)
	}
	resolved, ok := ref.(reference.Named)
	if !ok {
		return fmt.Errorf("resolved image name %q is not a named reference", name)
	}
	if hosts == nil {
		return fmt.Errorf("no registry hosts configured for %q", resolved)
	}

	refHost := reference.Domain(resolved)
	repository := reference.Path(resolved)
	if repository == "" {
		return fmt.Errorf("resolved image name %q has empty repository", name)
	}

	registryHosts, err := hosts(refHost)
	if err != nil {
		return fmt.Errorf("failed to resolve registry hosts for %q: %w", refHost, err)
	}
	if len(registryHosts) == 0 {
		return fmt.Errorf("no registry hosts available for %q", refHost)
	}
	deleteHosts := filterHostsByCapability(registryHosts, docker.HostCapabilityPush)
	if len(deleteHosts) == 0 {
		deleteHosts = registryHosts
	}

	var firstErr error
	for _, host := range deleteHosts {
		err := deleteDockerManifest(ctx, host, headers, refHost, repository, desc.Digest)
		if err == nil {
			return nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func filterHostsByCapability(hosts []docker.RegistryHost, cap docker.HostCapabilities) []docker.RegistryHost {
	filtered := make([]docker.RegistryHost, 0, len(hosts))
	for _, host := range hosts {
		if host.Capabilities.Has(cap) {
			filtered = append(filtered, host)
		}
	}
	return filtered
}

func deleteDockerManifest(ctx context.Context, host docker.RegistryHost, headers http.Header, refHost string, repository string, dgst digest.Digest) error {
	requestHeaders := http.Header{}
	if headers != nil {
		requestHeaders = headers.Clone()
	}
	for key, values := range host.Header {
		requestHeaders[key] = append(requestHeaders[key], values...)
	}

	deleteURL := url.URL{
		Scheme: host.Scheme,
		Host:   host.Host,
		Path:   path.Join("/", host.Path, repository, "manifests", dgst.String()),
	}
	addNamespace(&deleteURL, refHost, host.Host)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, deleteURL.String(), nil)
	if err != nil {
		return err
	}
	req.Header = requestHeaders

	client := &http.Client{}
	if host.Client != nil {
		*client = *host.Client
	}
	if client.CheckRedirect == nil {
		// Mimic containerd's resolver redirect handling.
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if host.Authorizer != nil {
				if err := host.Authorizer.Authorize(ctx, req); err != nil {
					return fmt.Errorf("failed to authorize redirect: %w", err)
				}
			}
			return nil
		}
	}

	resp, err := doDeleteRequest(ctx, client, req, host.Authorizer)
	if err != nil {
		return err
	}
	if resp.Body != nil {
		resp.Body.Close()
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("manifest %s not found: %w", dgst.String(), errdefs.ErrNotFound)
	}
	return fmt.Errorf("delete request failed: %s", resp.Status)
}

// doDeleteRequest mirrors containerd's retry/authorize request flow.
// Source: github.com/containerd/containerd/v2/core/remotes/docker/resolver.go
func doDeleteRequest(ctx context.Context, client *http.Client, req *http.Request, authorizer docker.Authorizer) (*http.Response, error) {
	const maxAttempts = 5
	var responses []*http.Response
	for range maxAttempts {
		cloned := req.Clone(ctx)
		if req.Header != nil {
			cloned.Header = req.Header.Clone()
		} else {
			cloned.Header = http.Header{}
		}
		if authorizer != nil {
			if err := authorizer.Authorize(ctx, cloned); err != nil {
				return nil, fmt.Errorf("failed to authorize: %w", err)
			}
		}
		resp, err := client.Do(cloned)
		if err != nil {
			return nil, fmt.Errorf("failed to do request: %w", err)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			return resp, nil
		}
		responses = append(responses, resp)
		resp.Body.Close()
		if authorizer == nil {
			return resp, nil
		}
		if err := authorizer.AddResponses(ctx, responses); err != nil {
			if errdefs.IsNotImplemented(err) {
				return resp, nil
			}
			return nil, err
		}
	}
	return nil, fmt.Errorf("authorization failed after %d attempts", maxAttempts)
}

// isProxy mirrors containerd's RegistryHost.isProxy logic.
// Source: github.com/containerd/containerd/v2/core/remotes/docker/registry.go
func isProxy(refHost string, registryHost string) bool {
	if refHost != registryHost {
		if refHost != "docker.io" || registryHost != "registry-1.docker.io" {
			return true
		}
	}
	return false
}

// addNamespace mirrors containerd's request.addNamespace behavior.
// Source: github.com/containerd/containerd/v2/core/remotes/docker/resolver.go
func addNamespace(target *url.URL, refHost string, registryHost string) {
	if !isProxy(refHost, registryHost) {
		return
	}
	query := target.Query()
	query.Set("ns", refHost)
	target.RawQuery = query.Encode()
}
