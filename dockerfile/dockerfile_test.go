// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package dockerfile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const digest = "sha256:82f6ff4a7f982960c8aa51617fea3aabc485df6e55cce80d22deb1de36f1bc3c"

func TestImages(t *testing.T) {
	tests := []struct {
		name       string
		dockerfile string
		want       []string
	}{
		{
			name:       "short name",
			dockerfile: "FROM busybox\n",
			want:       []string{"docker.io/library/busybox:latest"},
		},
		{
			name:       "tag and digest",
			dockerfile: "FROM registry:3.1.2@" + digest + "\n",
			want:       []string{"docker.io/library/registry:3.1.2@" + digest},
		},
		{
			name:       "digest only",
			dockerfile: "FROM ghcr.io/acme/base@" + digest + "\n",
			want:       []string{"ghcr.io/acme/base@" + digest},
		},
		{
			name:       "platform and stage name",
			dockerfile: "FROM --platform=$BUILDPLATFORM golang:1.27.3 AS build\n",
			want:       []string{"docker.io/library/golang:1.27.3"},
		},
		{
			name:       "scratch",
			dockerfile: "FROM golang:1.27.3 AS build\nFROM scratch\nCOPY --from=build /x /x\n",
			want:       []string{"docker.io/library/golang:1.27.3"},
		},
		{
			name:       "earlier stage",
			dockerfile: "FROM alpine:3.22 AS base\nFROM base\nFROM debian:bookworm\n",
			want:       []string{"docker.io/library/alpine:3.22", "docker.io/library/debian:bookworm"},
		},
		{
			name:       "stage name is lowercased",
			dockerfile: "FROM alpine:3.22 AS Base\nFROM base\n",
			want:       []string{"docker.io/library/alpine:3.22"},
		},
		{
			name:       "later stage",
			dockerfile: "FROM base\nFROM alpine:3.22 AS base\n",
			want:       []string{"docker.io/library/base:latest", "docker.io/library/alpine:3.22"},
		},
		{
			name:       "meta args",
			dockerfile: "ARG REPO=alpine\nARG IMAGE=${REPO}:3.22\nFROM $IMAGE\n",
			want:       []string{"docker.io/library/alpine:3.22"},
		},
		{
			name:       "comments and continuations",
			dockerfile: "# syntax=docker/dockerfile:1\n# an image\nFROM \\\n  alpine:3.22\n",
			want:       []string{"docker.io/library/alpine:3.22"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Images([]byte(tt.dockerfile))
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestImagesErrors(t *testing.T) {
	tests := []struct {
		name       string
		dockerfile string
	}{
		{name: "empty", dockerfile: ""},
		{name: "no from", dockerfile: "RUN true\n"},
		{name: "blank base", dockerfile: "ARG IMAGE\nFROM $IMAGE\n"},
		{name: "bad reference", dockerfile: "FROM Alpine\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Images([]byte(tt.dockerfile))
			require.Error(t, err)
		})
	}
}

func TestImage(t *testing.T) {
	got, err := Image([]byte("FROM registry:3.1.2@" + digest + "\n"))
	require.NoError(t, err)
	assert.Equal(t, "docker.io/library/registry:3.1.2@"+digest, got)

	_, err = Image([]byte("FROM scratch\n"))
	require.Error(t, err)

	_, err = Image([]byte("FROM alpine\nFROM debian\n"))
	require.Error(t, err)

	assert.Panics(t, func() { MustImage([]byte("FROM scratch\n")) })
}
