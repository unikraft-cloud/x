// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package buildkit

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigPrint(t *testing.T) {
	mtls := Config{Name: "default", Addr: "unix:///run/buildkit.sock", ServerName: "buildkitd", Dir: "/certs"}

	for _, tc := range []struct {
		name   string
		config Config
		format Format
		want   string
	}{
		{
			name:   "buildctl without mTLS",
			config: Config{Addr: "tcp://127.0.0.1:1234"},
			format: FormatBuildctl,
			want:   "buildctl --addr tcp://127.0.0.1:1234\n",
		},
		{
			name:   "buildctl with mTLS",
			config: mtls,
			format: FormatBuildctl,
			want:   "buildctl --addr unix:///run/buildkit.sock --tlsservername buildkitd --tlscacert /certs/ca.pem --tlscert /certs/cert.pem --tlskey /certs/key.pem\n",
		},
		{
			name:   "buildx with mTLS",
			config: mtls,
			format: FormatBuildx,
			want:   "docker buildx create --name default --driver remote --driver-opt cacert=/certs/ca.pem,cert=/certs/cert.pem,key=/certs/key.pem,servername=buildkitd unix:///run/buildkit.sock\n",
		},
		{
			name:   "buildx with a comma in the dir",
			config: Config{Addr: "unix:///run/buildkit.sock", ServerName: "buildkitd", Dir: "/a,b"},
			format: FormatBuildx,
			want:   `docker buildx create --driver remote --driver-opt '"cacert=/a,b/ca.pem","cert=/a,b/cert.pem","key=/a,b/key.pem",servername=buildkitd' unix:///run/buildkit.sock` + "\n",
		},
		{
			name:   "env with mTLS quotes the dir",
			config: Config{Addr: "unix:///run/buildkit.sock", ServerName: "buildkitd", Dir: "/it's certs"},
			format: FormatEnv,
			want:   "export BUILDKIT_HOST=unix:///run/buildkit.sock\nbuildctl --tlsdir '/it'\\''s certs' --tlsservername buildkitd\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			require.NoError(t, tc.config.Print(&b, tc.format))
			assert.Equal(t, tc.want, b.String())
		})
	}
}

func TestConfigPrintUnknownFormat(t *testing.T) {
	assert.Error(t, Config{}.Print(&strings.Builder{}, "podman"))
}
