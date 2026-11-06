// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !js

package buildkit

import (
	"context"
	"fmt"
	"net"

	"github.com/moby/buildkit/client"
	dockerclient "github.com/moby/moby/client"
)

// Docker is the buildkitd built into the docker daemon that DOCKER_HOST
// points to, which is unavailable when docker does not answer.
type Docker struct{}

// Acquire implements Backend. Docker has a single builder, so key is ignored.
//
// See https://github.com/docker/buildx/blob/master/driver/docker/driver.go.
func (Docker) Acquire(ctx context.Context, _ Key) (*Daemon, error) {
	d, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("%w in docker: %w", ErrUnavailable, err)
	}
	if _, err := d.ServerVersion(ctx, dockerclient.ServerVersionOptions{}); err != nil {
		d.Close()
		return nil, fmt.Errorf("%w in docker: %w", ErrUnavailable, err)
	}

	c, err := client.New(ctx, "",
		client.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return d.DialHijack(ctx, "/grpc", "h2c", nil)
		}),
		client.WithSessionDialer(func(ctx context.Context, proto string, meta map[string][]string) (net.Conn, error) {
			return d.DialHijack(ctx, "/session", proto, meta)
		}),
	)
	if err != nil {
		d.Close()
		return nil, fmt.Errorf("connecting to docker's buildkit: %w", err)
	}
	if _, err := c.Info(ctx); err != nil {
		c.Close()
		d.Close()
		return nil, fmt.Errorf("%w in docker: %w", ErrUnavailable, err)
	}
	return &Daemon{Client: c, Addr: d.DaemonHost(), docker: d}, nil
}
