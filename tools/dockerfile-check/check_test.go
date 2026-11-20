// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const digest = "sha256:82f6ff4a7f982960c8aa51617fea3aabc485df6e55cce80d22deb1de36f1bc3c"

func TestUnpinned(t *testing.T) {
	tests := []struct {
		name       string
		dockerfile string
		allow      []string
		want       []string
	}{
		{
			name:       "tag and digest",
			dockerfile: "FROM registry:3.1.2@" + digest + "\n",
		},
		{
			name:       "tag only",
			dockerfile: "FROM registry:3.1.2\n",
			want:       []string{"docker.io/library/registry:3.1.2"},
		},
		{
			name:       "digest only",
			dockerfile: "FROM ghcr.io/acme/base@" + digest + "\n",
			want:       []string{"ghcr.io/acme/base@" + digest},
		},
		{
			name:       "neither",
			dockerfile: "FROM busybox\n",
			want:       []string{"docker.io/library/busybox:latest"},
		},
		{
			name:       "scratch and earlier stage",
			dockerfile: "FROM golang:1.27.4@" + digest + " AS build\nFROM build\nFROM scratch\n",
		},
		{
			name:       "meta arg",
			dockerfile: "ARG IMAGE=alpine:3.22\nFROM $IMAGE\n",
			want:       []string{"docker.io/library/alpine:3.22"},
		},
		{
			name:       "allowed prefix",
			dockerfile: "FROM ghcr.io/acme/base:1\nFROM ghcr.io/other/base:1\n",
			allow:      []string{"ghcr.io/acme/"},
			want:       []string{"ghcr.io/other/base:1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := unpinned([]byte(tt.dockerfile), tt.allow)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestUnpinnedError(t *testing.T) {
	_, err := unpinned([]byte("ARG IMAGE\nFROM $IMAGE\n"), nil)
	require.Error(t, err)
}
