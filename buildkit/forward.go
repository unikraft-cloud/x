// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !js

package buildkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	"unikraft.com/x/log"
)

// Forward sends each connection on ln through the platform's edge TLS to the
// builder until ctx is done, then closes ln. The client does buildkit's mTLS.
func (d *Daemon) Forward(ctx context.Context, ln net.Listener) error {
	defer ln.Close()
	if d.edge == nil {
		return fmt.Errorf("forwarding to %s: %w", d.Addr, errors.ErrUnsupported)
	}
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()

	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go d.forward(ctx, c)
	}
}

// forward copies between c and a new connection to the builder, and closes
// both when one side stops or ctx is done.
func (d *Daemon) forward(ctx context.Context, c net.Conn) {
	defer c.Close()
	e, err := d.edge(ctx)
	if err != nil {
		log.G(ctx).Warn().Err(err).Str("addr", d.Addr).Msg("could not connect to the builder")
		return
	}
	defer e.Close()
	stop := context.AfterFunc(ctx, func() {
		c.Close()
		e.Close()
	})
	defer stop()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(e, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, e); done <- struct{}{} }()
	<-done
}
