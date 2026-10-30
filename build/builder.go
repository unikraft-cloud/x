// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package build makes Unikraft images from a runtime, a rootfs and roms.
package build

import (
	"context"
	"errors"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	imagespec "unikraft.com/x/image-spec"

	"unikraft.com/x/build/progress"
)

// Builder makes Unikraft images. It calls each of its functions only when a
// step needs it.
type Builder struct {
	// Images gives the accessor for runtime kernels and OCI rootfs sources. When
	// it is nil, the build uses a default accessor without credentials.
	Images func(ctx context.Context) (*imagespec.Accessor, error)

	// Buildkit connects one time for each rootfs that needs a daemon. The build
	// calls cleanup, which can be nil, and does not close the client.
	Buildkit func(ctx context.Context) (*client.Client, func(), error)

	// Session gives the attachables of each solve, such as registry auth, but no
	// FileSync when a source is local. When nil, BuildKit pulls without credentials.
	Session func(ctx context.Context) ([]session.Attachable, error)

	// SharedKey is the shared key of each solve session. Give each caller directory
	// its own key. A sync with a known key then sends only the changed files.
	SharedKey string

	// Progress takes every step of the build as a buildkit SolveStatus,
	// the ones outside the daemon included. When it is nil, the build
	// discards its progress.
	Progress progress.Writer

	// TempDir holds the intermediate files. Empty means os.TempDir.
	TempDir string

	// MaxRootfsSize is the maximum number of bytes of each rootfs or rom that
	// the build gets before it packs it. Zero means no limit.
	MaxRootfsSize int64
}

func (b Builder) images(ctx context.Context) (*imagespec.Accessor, error) {
	if b.Images == nil {
		return imagespec.NewAccessor(), nil
	}
	access, err := b.Images(ctx)
	if err != nil {
		return nil, err
	}
	if access == nil {
		return nil, errors.New("image callback gave no accessor")
	}
	return access, nil
}

// progressWriter returns where the build reports its steps.
func (b Builder) progressWriter() progress.Writer {
	if b.Progress == nil {
		return progress.Discard
	}
	return b.Progress
}
