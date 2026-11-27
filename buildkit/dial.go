// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !js

package buildkit

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"unikraft.com/cloud/sdk/platform"
)

// Acquire implements Backend. It creates the builder for key with a cache
// volume if there is none, and starts it if it was stopped or in standby.
func (p *Platform) Acquire(ctx context.Context, key Key) (*Daemon, error) {
	name := "remote-builder-" + cmp.Or(key.Name, "default")
	// The builder may be booting, waking from standby or shutting down.
	wctx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()

	inst, err := p.instance(wctx, name, key)
	if err != nil {
		return nil, err
	}

	if inst.ServiceGroup == nil || len(inst.ServiceGroup.Domains) == 0 {
		return nil, fmt.Errorf("builder %s has no domain, delete it to recreate it", name)
	}
	t, err := clientTLS(inst.Env)
	if err != nil {
		return nil, fmt.Errorf("builder %s: %w, delete it to recreate it", name, err)
	}
	addr := net.JoinHostPort(strings.TrimSuffix(inst.ServiceGroup.Domains[0].Fqdn, "."), strconv.Itoa(port))
	edge, err := edgeDialer(addr, p.insecure)
	if err != nil {
		return nil, fmt.Errorf("connecting to builder %s: %w", name, err)
	}
	c, err := dial(ctx, addr, t, edge)
	if err != nil {
		return nil, fmt.Errorf("connecting to builder %s: %w", name, err)
	}

	// The platform knows why a builder does not start long before the wait ends.
	live, stop := context.WithCancelCause(wctx)
	defer stop(nil)
	go func() {
		for {
			select {
			case <-live.Done():
				return
			case <-time.After(time.Second):
			}
			switch cur, err := p.get(live, name); {
			case err != nil:
			case cur == nil:
				stop(errors.New("the builder was deleted"))
			// Until the start shows, the stop is still the one of before.
			case (cur.State == platform.InstanceStateStopped || cur.State == platform.InstanceStateStandby) && cur.StoppedAt != nil && (inst.StoppedAt == nil || !cur.StoppedAt.Equal(*inst.StoppedAt)):
				stop(fmt.Errorf("the builder stopped: %s", cur.DescribeStop()))
			}
		}
	}()

	if err := c.Wait(live); err != nil {
		// gRPC does not always return the cause of a wait that the platform ended.
		if wctx.Err() == nil && live.Err() != nil {
			c.Close()
			return nil, fmt.Errorf("waiting for builder %s: %w", name, context.Cause(live))
		}
		// Wait drops the connection errors it retries, so ask once more for the last one.
		pctx, cancel := context.WithTimeout(ctx, time.Second)
		_, perr := c.ControlClient().Info(pctx, &controlapi.InfoRequest{})
		cancel()
		if perr != nil {
			c.Close()
			return nil, fmt.Errorf("waiting for builder %s: %w: %w", name, err, perr)
		}
	}
	return &Daemon{Client: c, Addr: addr, TLS: t, edge: edge}, nil
}

// dial connects to buildkitd at addr with buildkit's mTLS inside the TLS that
// the platform's edge terminates.
func dial(ctx context.Context, addr string, t TLS, edge func(context.Context) (net.Conn, error)) (*client.Client, error) {
	inner, err := t.config()
	if err != nil {
		return nil, err
	}
	return client.New(ctx, "tcp://"+addr,
		client.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return edge(ctx)
		}),
		client.WithGRPCDialOption(grpc.WithTransportCredentials(credentials.NewTLS(inner))),
		client.WithGRPCDialOption(grpc.WithAuthority(t.ServerName)),
		// A builder that is gone answers no ping. buildkitd refuses more than one each 5 minutes.
		client.WithGRPCDialOption(grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 5 * time.Minute, Timeout: 20 * time.Second})),
	)
}

// edgeDialer returns a function that opens a connection to addr through the
// TLS that the platform's edge terminates.
func edgeDialer(addr string, insecure bool) (func(context.Context) (net.Conn, error), error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	d := &tls.Dialer{Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecure}}
	return func(ctx context.Context) (net.Conn, error) {
		return d.DialContext(ctx, "tcp", addr)
	}, nil
}
