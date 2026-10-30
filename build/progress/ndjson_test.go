// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package progress_test

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/build/progress"
)

// The lines of a stream, as `docker buildx build --progress=rawjson` writes
// them and the build service's API shows them.
const (
	lineKernel = `{"vertexes":[{"digest":"sha256:a1","name":"[kernel] pull unikraft.org/base-compat:latest","started":"2026-10-09T12:00:01Z","completed":"2026-10-09T12:00:02Z"}]}`
	lineRootfs = `{"vertexes":[{"digest":"sha256:b2","name":"[rootfs 2/4] RUN npm ci","started":"2026-10-09T12:00:03Z"}]}`
	lineLog    = `{"logs":[{"vertex":"sha256:b2","stream":1,"data":"YWRkZWQgMjEyIHBhY2thZ2Vz","timestamp":"2026-10-09T12:00:20Z"}]}`
	lineFailed = `{"vertexes":[{"digest":"sha256:b2","name":"[rootfs 2/4] RUN npm ci","started":"2026-10-09T12:00:03Z","completed":"2026-10-09T12:00:21Z","error":"exit code: 1"}]}`
)

// at gives a time in the minute the lines above are from.
func at(second int) time.Time {
	return time.Date(2026, 10, 9, 12, 0, second, 0, time.UTC)
}

func TestEncoder(t *testing.T) {
	t.Run("a status is one line in buildx's rawjson shape", func(t *testing.T) {
		var out bytes.Buffer
		enc := progress.NewEncoder(&out)

		require.NoError(t, enc.Encode(&client.SolveStatus{Vertexes: []*client.Vertex{{
			Digest:    "sha256:a1",
			Name:      "[kernel] pull unikraft.org/base-compat:latest",
			Started:   new(at(1)),
			Completed: new(at(2)),
		}}}))
		require.NoError(t, enc.Encode(&client.SolveStatus{Logs: []*client.VertexLog{{
			Vertex:    "sha256:b2",
			Stream:    1,
			Data:      []byte("added 212 packages"),
			Timestamp: at(20),
		}}}))

		assert.Equal(t, []string{lineKernel, lineLog, ""}, strings.Split(out.String(), "\n"))
	})
}

func TestDecoder(t *testing.T) {
	t.Run("what was encoded is decoded back", func(t *testing.T) {
		want := &client.SolveStatus{
			Vertexes: []*client.Vertex{{
				Digest:    "sha256:b2",
				Inputs:    []digest.Digest{"sha256:a1"},
				Name:      "[rootfs 2/4] RUN npm ci",
				Started:   new(at(3)),
				Completed: new(at(21)),
				Cached:    true,
			}},
			Statuses: []*client.VertexStatus{{
				ID:        "extracting",
				Vertex:    "sha256:b2",
				Name:      "sha256:c3",
				Total:     100,
				Current:   42,
				Timestamp: at(10),
				Started:   new(at(4)),
			}},
			Logs: []*client.VertexLog{{
				Vertex:    "sha256:b2",
				Stream:    2,
				Data:      []byte("npm WARN deprecated\n"),
				Timestamp: at(20),
			}},
			Warnings: []*client.VertexWarning{{
				Vertex: "sha256:b2",
				Level:  1,
				Short:  []byte("FromAsCasing"),
				Detail: [][]byte{[]byte("'as' and 'FROM' keywords' casing do not match")},
				URL:    "https://docs.docker.com/go/dockerfile/rule/from-as-casing/",
			}},
		}

		var stream bytes.Buffer
		require.NoError(t, progress.NewEncoder(&stream).Encode(want))

		got, err := progress.NewDecoder(&stream).Decode()
		require.NoError(t, err)
		assert.Equal(t, want, got)
	})

	t.Run("the stream ends with io.EOF", func(t *testing.T) {
		dec := progress.NewDecoder(strings.NewReader(lineKernel + "\n"))

		ss, err := dec.Decode()
		require.NoError(t, err)
		assert.Len(t, ss.Vertexes, 1)

		_, err = dec.Decode()
		require.ErrorIs(t, err, io.EOF)
	})

	t.Run("a stream cut in the middle of a line is an unexpected EOF", func(t *testing.T) {
		dec := progress.NewDecoder(strings.NewReader(lineKernel[:40]))

		_, err := dec.Decode()
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	})

	t.Run("null entries are dropped", func(t *testing.T) {
		line := `{"vertexes":[null,{"digest":"sha256:a1"}],"statuses":[null],"logs":[null],"warnings":[null]}`

		ss, err := progress.NewDecoder(strings.NewReader(line)).Decode()
		require.NoError(t, err)
		require.Len(t, ss.Vertexes, 1)
		assert.Equal(t, "sha256:a1", string(ss.Vertexes[0].Digest))
		assert.Empty(t, ss.Statuses)
		assert.Empty(t, ss.Logs)
		assert.Empty(t, ss.Warnings)
	})
}
