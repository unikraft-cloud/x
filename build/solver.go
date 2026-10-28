// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/moby/buildkit/client"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"

	ukio "unikraft.com/x/io"

	"unikraft.com/x/build/progress"
)

// solver holds one BuildKit connection for several solves.
type solver struct {
	client    *client.Client
	cleanup   func()
	session   []session.Attachable
	sharedKey string
	progress  progress.Writer
	maxSize   int64 // of each tarball, 0 for no limit
}

func (b Builder) newSolver(ctx context.Context) (*solver, error) {
	if b.Buildkit == nil {
		return nil, errors.New("no buildkit connector configured")
	}

	var attachables []session.Attachable
	if b.Session != nil {
		var err error
		attachables, err = b.Session(ctx)
		if err != nil {
			return nil, err
		}
	}

	c, cleanup, err := b.Buildkit(ctx)
	if err != nil {
		return nil, err
	}
	if c == nil {
		if cleanup != nil {
			cleanup()
		}
		return nil, errors.New("buildkit connector gave no client")
	}

	return &solver{
		client:    c,
		cleanup:   cleanup,
		session:   slices.Clone(attachables),
		sharedKey: b.SharedKey,
		progress:  b.progressWriter(),
		maxSize:   b.MaxRootfsSize,
	}, nil
}

func (s *solver) close() {
	if s.cleanup != nil {
		s.cleanup()
	}
}

// solveToTar runs build, exporting an uncompressed tarball of the result to
// dst, which is left synced and rewound. The progress of the solve is
// reported under prefix. A tarball past the size limit fails with ErrTooLarge.
func (s *solver) solveToTar(ctx context.Context, dst *os.File, prefix string, solveOpt client.SolveOpt, build gateway.BuildFunc) error {
	solveOpt.Ref = identity.NewID()
	solveOpt.Session = s.session
	solveOpt.SharedKey = s.sharedKey
	out := &limitWriter{w: dst, max: s.maxSize}
	solveOpt.Exports = []client.ExportEntry{{
		Type: client.ExporterTar,
		Output: func(map[string]string) (io.WriteCloser, error) {
			return ukio.NopWriteCloser(out), nil
		},
	}}

	// Build closes the status channel when it returns, however it went, and
	// the last statuses on it are what say how, so they are forwarded before
	// the error is looked at.
	statuses, done := progress.NewChannel(progress.WithPrefix(s.progress, prefix))
	_, err := s.client.Build(ctx, solveOpt, "buildctl", build, statuses)
	<-done
	if err != nil {
		return out.wrap(err)
	}

	if err := dst.Sync(); err != nil {
		return fmt.Errorf("could not sync tarball: %w", err)
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("could not rewind tarball: %w", err)
	}

	return nil
}
