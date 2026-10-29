// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !js

package buildkit

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/appdefaults"

	_ "github.com/moby/buildkit/client/connhelper/dockercontainer"
	_ "github.com/moby/buildkit/client/connhelper/kubepod"
	_ "github.com/moby/buildkit/client/connhelper/nerdctlcontainer"
	_ "github.com/moby/buildkit/client/connhelper/npipe"
	_ "github.com/moby/buildkit/client/connhelper/podmancontainer"
	_ "github.com/moby/buildkit/client/connhelper/ssh"
)

// probeTimeout bounds the Info probe, which grpc hangs on a proxy that never
// answers its CONNECT.
const probeTimeout = 10 * time.Second

// Host is the buildkitd at Addr, else at BUILDKIT_HOST, else on buildkit's
// default socket, which is unavailable when nothing answers there and on
// Windows, where it is a native buildkitd that cannot build Linux images.
type Host struct {
	Addr string
}

// Acquire implements Backend. Host has a single builder, so key is ignored.
func (h Host) Acquire(ctx context.Context, _ Key) (*Daemon, error) {
	addr := cmp.Or(h.Addr, os.Getenv("BUILDKIT_HOST"))
	explicit := addr != ""
	if !explicit {
		addr = appdefaults.Address
		if runtime.GOOS == "windows" {
			return nil, fmt.Errorf("%w at %s: a native windows buildkitd cannot build linux images", ErrUnavailable, addr)
		}
	}

	c, err := client.New(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("connecting to buildkit at %s: %w", addr, err)
	}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if _, err := c.Info(pctx); err != nil {
		c.Close()
		if !explicit {
			return nil, fmt.Errorf("%w at %s: %w", ErrUnavailable, addr, err)
		}
		return nil, fmt.Errorf("connecting to buildkit at %s: %w", addr, err)
	}
	return &Daemon{Client: c, Addr: addr}, nil
}
