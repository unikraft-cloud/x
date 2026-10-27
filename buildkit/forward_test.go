// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !js

package buildkit

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForward(t *testing.T) {
	env, err := generateCerts()
	require.NoError(t, err)
	client, err := clientTLS(env)
	require.NoError(t, err)

	// The edge terminates its TLS, then buildkitd does mTLS and echoes.
	srvCfg := serverConfig(t, env)
	edgeLn, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: srvCfg.Certificates})
	require.NoError(t, err)
	t.Cleanup(func() { edgeLn.Close() })
	go func() {
		c, err := edgeLn.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		srv := tls.Server(c, srvCfg)
		_, _ = io.Copy(srv, srv)
	}()

	edge, err := edgeDialer(edgeLn.Addr().String(), true)
	require.NoError(t, err)
	d := &Daemon{TLS: client, edge: edge}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- d.Forward(ctx, ln) }()

	cfg, err := client.config()
	require.NoError(t, err)
	c, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer c.Close()
	conn := tls.Client(c, cfg)
	_, err = conn.Write([]byte("ping"))
	require.NoError(t, err)
	got := make([]byte, 4)
	_, err = io.ReadFull(conn, got)
	require.NoError(t, err)
	assert.Equal(t, "ping", string(got))

	cancel()
	assert.NoError(t, <-done)
}

func TestForwardWithoutEdge(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	err = (&Daemon{Addr: "unix:///run/buildkit/buildkitd.sock"}).Forward(t.Context(), ln)
	assert.ErrorIs(t, err, errors.ErrUnsupported)
}
