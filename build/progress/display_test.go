// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package progress_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/progress/progressui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/build/progress"
)

// startedVertex is a status of one vertex named name that has started, which
// is when progressui first draws it.
func startedVertex(name string) *client.SolveStatus {
	return &client.SolveStatus{Vertexes: []*client.Vertex{{
		Digest:  "sha256:a1",
		Name:    name,
		Started: new(at(1)),
	}}}
}

func TestPlain(t *testing.T) {
	t.Run("the stream renders as buildx's plain progress", func(t *testing.T) {
		stream := strings.Join([]string{lineKernel, lineRootfs, lineLog, lineFailed}, "\n") + "\n"

		var out bytes.Buffer
		require.NoError(t, progress.Plain(t.Context(), &out, strings.NewReader(stream)))

		assert.Contains(t, out.String(), "#1 [kernel] pull unikraft.org/base-compat:latest\n")
		assert.Contains(t, out.String(), "#1 DONE 1.0s\n")
		assert.Contains(t, out.String(), "#2 [rootfs 2/4] RUN npm ci\n")
		assert.Contains(t, out.String(), "added 212 packages")
		assert.Contains(t, out.String(), "#2 ERROR: exit code: 1\n")
	})

	t.Run("a context that ended stops the render at the next status", func(t *testing.T) {
		errGone := errors.New("the watcher went away")
		ctx, cancel := context.WithCancelCause(t.Context())
		cancel(errGone)

		var out bytes.Buffer
		err := progress.Plain(ctx, &out, strings.NewReader(lineKernel+"\n"+lineRootfs+"\n"))

		require.ErrorIs(t, err, errGone)
		assert.NotContains(t, out.String(), "[kernel]")
	})

	t.Run("a line that is not a status stops the render with its error", func(t *testing.T) {
		var out bytes.Buffer
		err := progress.Plain(t.Context(), &out, strings.NewReader(lineKernel+"\nnot json\n"))

		require.Error(t, err)
		require.NotErrorIs(t, err, io.EOF)
		assert.Contains(t, out.String(), "#1 [kernel] pull unikraft.org/base-compat:latest\n")
	})
}

func TestDisplay(t *testing.T) {
	t.Run("a display whose context ended still renders and says so on close", func(t *testing.T) {
		errGone := errors.New("the watcher went away")
		ctx, cancel := context.WithCancelCause(t.Context())
		cancel(errGone)

		var out bytes.Buffer
		d, err := progress.NewDisplay(ctx, &out, progressui.PlainMode)
		require.NoError(t, err)

		d.Write(startedVertex("[kernel] pull base-compat:latest"))
		require.ErrorIs(t, d.Close(), errGone)
		assert.Contains(t, out.String(), "#1 [kernel] pull base-compat:latest\n")
	})
}
