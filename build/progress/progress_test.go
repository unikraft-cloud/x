// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package progress_test

import (
	"errors"
	"testing"

	"github.com/moby/buildkit/client"
	resourcestypes "github.com/moby/buildkit/executor/resources/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/build/progress"
)

// recorder keeps every status written to it.
type recorder struct {
	statuses []*client.SolveStatus
	usage    []progress.Usage
}

func (r *recorder) Write(ss *client.SolveStatus) {
	r.statuses = append(r.statuses, ss)
}

func (r *recorder) WriteUsage(u progress.Usage) {
	r.usage = append(r.usage, u)
}

// vertices returns the vertices of every status, in order.
func (r *recorder) vertices() []*client.Vertex {
	var vs []*client.Vertex
	for _, ss := range r.statuses {
		vs = append(vs, ss.Vertexes...)
	}
	return vs
}

// vertex is a status of one vertex named name.
func vertex(name string) *client.SolveStatus {
	return &client.SolveStatus{Vertexes: []*client.Vertex{{Digest: "sha256:a1", Name: name}}}
}

func TestStep(t *testing.T) {
	t.Run("a step is reported when it starts and when it ends", func(t *testing.T) {
		var rec recorder

		err := progress.Step(&rec, "[kernel] pull base-compat:latest", func() error {
			assert.Len(t, rec.vertices(), 1, "the step is reported before it runs")
			return nil
		})
		require.NoError(t, err)

		vs := rec.vertices()
		require.Len(t, vs, 2)
		started, completed := vs[0], vs[1]

		assert.Equal(t, "[kernel] pull base-compat:latest", started.Name)
		assert.NotEmpty(t, started.Digest)
		assert.NotNil(t, started.Started)
		assert.Nil(t, started.Completed)

		assert.Equal(t, started.Digest, completed.Digest)
		assert.Equal(t, started.Name, completed.Name)
		assert.Equal(t, started.Started, completed.Started)
		require.NotNil(t, completed.Completed)
		assert.False(t, completed.Completed.Before(*completed.Started))
		assert.Empty(t, completed.Error)
	})

	t.Run("a failed step carries its error", func(t *testing.T) {
		var rec recorder
		errPull := errors.New("no such image")

		err := progress.Step(&rec, "[kernel] pull nope", func() error { return errPull })
		require.ErrorIs(t, err, errPull)

		vs := rec.vertices()
		require.Len(t, vs, 2)
		assert.Empty(t, vs[0].Error)
		assert.Equal(t, "no such image", vs[1].Error)
		assert.NotNil(t, vs[1].Completed)
	})

	t.Run("two steps of the same name are told apart", func(t *testing.T) {
		var rec recorder

		require.NoError(t, progress.Step(&rec, "[pack] rom as erofs", func() error { return nil }))
		require.NoError(t, progress.Step(&rec, "[pack] rom as erofs", func() error { return nil }))

		vs := rec.vertices()
		require.Len(t, vs, 4)
		assert.NotEqual(t, vs[0].Digest, vs[2].Digest)
	})
}

func TestWithPrefix(t *testing.T) {
	t.Run("the prefix goes in brackets before the name", func(t *testing.T) {
		var rec recorder

		progress.WithPrefix(&rec, "rootfs").Write(vertex("pull alpine:latest"))

		require.Len(t, rec.vertices(), 1)
		assert.Equal(t, "[rootfs] pull alpine:latest", rec.vertices()[0].Name)
	})

	t.Run("a name with a bracket of its own shares it", func(t *testing.T) {
		var rec recorder
		w := progress.WithPrefix(&rec, "rootfs")

		w.Write(vertex("[2/4] RUN npm ci"))
		w.Write(vertex("[internal] load build definition from Dockerfile"))

		vs := rec.vertices()
		require.Len(t, vs, 2)
		assert.Equal(t, "[rootfs 2/4] RUN npm ci", vs[0].Name)
		assert.Equal(t, "[rootfs internal] load build definition from Dockerfile", vs[1].Name)
	})

	t.Run("the status given is left as it is", func(t *testing.T) {
		var rec recorder
		ss := vertex("[2/4] RUN npm ci")

		progress.WithPrefix(&rec, "rootfs").Write(ss)

		assert.Equal(t, "[2/4] RUN npm ci", ss.Vertexes[0].Name)
		assert.Equal(t, "sha256:a1", string(ss.Vertexes[0].Digest))
	})

	t.Run("the same vertex under two prefixes is two vertices, each with its logs", func(t *testing.T) {
		var rec recorder
		log := &client.SolveStatus{Logs: []*client.VertexLog{{Vertex: "sha256:a1", Data: []byte("hi")}}}

		for _, prefix := range []string{"rootfs fc/x86_64", "rootfs qemu/x86_64"} {
			w := progress.WithPrefix(&rec, prefix)
			w.Write(vertex("[2/4] RUN npm ci"))
			w.Write(log)
		}

		require.Len(t, rec.statuses, 4)
		fc, qemu := rec.statuses[0].Vertexes[0], rec.statuses[2].Vertexes[0]
		assert.NotEqual(t, fc.Digest, qemu.Digest)
		assert.Equal(t, fc.Digest, rec.statuses[1].Logs[0].Vertex)
		assert.Equal(t, qemu.Digest, rec.statuses[3].Logs[0].Vertex)
	})

	t.Run("usage is keyed by the digest the vertex got", func(t *testing.T) {
		var rec recorder
		w := progress.WithPrefix(&rec, "rootfs")
		samples := &resourcestypes.Samples{Samples: []*resourcestypes.Sample{{PIDsStat: &resourcestypes.PIDsStat{Current: new(uint64(3))}}}}

		w.Write(vertex("[2/4] RUN npm ci"))
		w.WriteUsage(progress.Usage{"sha256:a1": samples})
		progress.WithPrefix(progress.Discard, "rootfs").WriteUsage(progress.Usage{"sha256:a1": samples})

		require.Len(t, rec.usage, 1)
		assert.Equal(t, progress.Usage{rec.vertices()[0].Digest: samples}, rec.usage[0])
	})
}

func TestNewChannel(t *testing.T) {
	t.Run("statuses are forwarded in order until the channel is closed", func(t *testing.T) {
		var rec recorder
		statuses, done := progress.NewChannel(&rec)

		first, second := vertex("one"), vertex("two")
		statuses <- first
		statuses <- second
		close(statuses)
		<-done

		require.Len(t, rec.statuses, 2)
		assert.Same(t, first, rec.statuses[0])
		assert.Same(t, second, rec.statuses[1])
	})
}
