// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/moby/buildkit/util/contentutil"
)

// HelloWorld is a regular OCI image, with plain layers and no unikraft
// components.
var HelloWorld = &MirroredImage{
	Name:   "hello-world",
	Source: "index.docker.io/library/hello-world:latest",
}

// MirroredImage is a public image that tests copy into their own
// organization, so that each user runs against their own project. The copy
// stays there: all runs share its digest, thus a delete breaks the other runs.
type MirroredImage struct {
	// Name is the image name, without the organization and the tag.
	Name string
	// Source is the full reference of the image to copy.
	Source string

	once sync.Once
	ref  string
	err  error
}

// Mirror copies the image with resolver into namespace, such as
// unikraft.io/<organization>, one time and gives the full reference. Later
// calls give the reference of the first copy.
func (m *MirroredImage) Mirror(t testing.TB, ctx context.Context, resolver remotes.Resolver, namespace string) string {
	t.Helper()
	m.once.Do(func() {
		m.ref, m.err = m.mirror(ctx, resolver, namespace)
	})
	if m.err != nil {
		t.Fatal(m.err)
	}
	return m.ref
}

// mirrorTag is the tag of every copy. A push of the same content changes nothing.
const mirrorTag = "test"

// mirror copies every manifest and blob of the source image, so that a
// multi-platform image keeps all of its platforms.
func (m *MirroredImage) mirror(ctx context.Context, resolver remotes.Resolver, namespace string) (string, error) {
	ref := namespace + "/" + m.Name + ":" + mirrorTag

	_, desc, err := resolver.Resolve(ctx, m.Source)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", m.Source, err)
	}
	// The copy of an earlier run is good when it has the digest of the source.
	if _, copied, err := resolver.Resolve(ctx, ref); err == nil && copied.Digest == desc.Digest {
		return ref, nil
	}
	fetcher, err := resolver.Fetcher(ctx, m.Source)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", m.Source, err)
	}
	// The digest in the reference gives the tag to the root manifest only.
	pusher, err := resolver.Pusher(ctx, ref+"@"+desc.Digest.String())
	if err != nil {
		return "", fmt.Errorf("pushing to %s: %w", ref, err)
	}
	if err := contentutil.CopyChain(ctx, contentutil.FromPusher(pusher), contentutil.FromFetcher(fetcher), desc); err != nil {
		return "", fmt.Errorf("mirroring %s to %s: %w", m.Source, ref, err)
	}
	return ref, nil
}
