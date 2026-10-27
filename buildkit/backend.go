// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !js

// Package buildkit connects a buildkit client to a buildkitd: one on the
// host, docker's, or one it finds or creates on Unikraft Cloud.
package buildkit

import (
	"cmp"
	"context"
	"errors"
	"io"
	"net"

	"github.com/moby/buildkit/client"
)

// ErrUnavailable is returned by a Backend that has no buildkitd to offer.
var ErrUnavailable = errors.New("buildkit unavailable")

// Backend provides buildkitds.
type Backend interface {
	// Acquire connects to the buildkitd for key.
	Acquire(ctx context.Context, key Key) (*Daemon, error)
}

// Fallback acquires from the first of its backends that is not unavailable.
type Fallback []Backend

// Acquire implements Backend.
func (f Fallback) Acquire(ctx context.Context, key Key) (*Daemon, error) {
	var errs []error
	for _, b := range f {
		d, err := b.Acquire(ctx, key)
		if !errors.Is(err, ErrUnavailable) {
			return d, err
		}
		errs = append(errs, err)
	}
	return nil, cmp.Or(errors.Join(errs...), ErrUnavailable)
}

// Daemon is a builder with a connected client.
type Daemon struct {
	Client *client.Client

	// Addr is the builder's address.
	Addr string

	// TLS is the CA, certificate and key for buildkit's mTLS, which runs inside
	// the TLS that the platform's edge terminates on Addr. It is empty for other
	// backends.
	TLS TLS

	// docker, if set, carries Client's connection.
	docker io.Closer

	// edge, if set, opens a connection to the builder through the TLS that the
	// platform's edge terminates.
	edge func(ctx context.Context) (net.Conn, error)
}

// Release closes the daemon's client.
func (d *Daemon) Release() error {
	err := d.Client.Close()
	if d.docker != nil {
		err = errors.Join(err, d.docker.Close())
	}
	return err
}
