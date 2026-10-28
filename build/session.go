// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/tonistiigi/fsutil"
	fstypes "github.com/tonistiigi/fsutil/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// The headers that announce a session to the daemon, as session.Session.Run
// sends them.
const (
	headerSessionID        = "X-Docker-Expose-Session-Uuid"
	headerSessionSharedKey = "X-Docker-Expose-Session-Sharedkey"
	headerSessionMethod    = "X-Docker-Expose-Session-Grpc-Method"
)

// serveSession serves attachables to the daemon of c until stop. Unlike
// session.Session, gRPC's own transport only opens a stream's window as it is read.
func serveSession(ctx context.Context, c *client.Client, sharedKey string, attachables []session.Attachable) (_ *session.Session, stop func(), _ error) {
	// The solve only takes the ID of this session.
	s, err := session.NewSession(ctx, sharedKey)
	if err != nil {
		return nil, nil, fmt.Errorf("could not create session: %w", err)
	}

	srv := grpc.NewServer(
		grpc.UnaryInterceptor(grpcerrors.UnaryServerInterceptor),
		grpc.StreamInterceptor(grpcerrors.StreamServerInterceptor),
	)
	grpc_health_v1.RegisterHealthServer(srv, health.NewServer())
	for _, a := range attachables {
		a.Register(srv)
	}

	meta := map[string][]string{
		headerSessionID:        {s.ID()},
		headerSessionSharedKey: {sharedKey},
	}
	for name, svc := range srv.GetServiceInfo() {
		for _, m := range svc.Methods {
			meta[headerSessionMethod] = append(meta[headerSessionMethod], session.MethodURL(name, m.Name))
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	conn, err := c.Dialer()(ctx, "h2c", meta)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("could not dial session: %w", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(newConnListener(conn))
	}()
	return s, func() {
		srv.Stop()
		cancel()
		<-done
	}, nil
}

// syncedDirs returns mounts with the owners that the solve gives to its
// LocalMounts.
func syncedDirs(mounts map[string]fsutil.FS) (filesync.StaticDirSource, error) {
	dirs := make(filesync.StaticDirSource, len(mounts))
	for name, mount := range mounts {
		fs, err := fsutil.NewFilterFS(mount, &fsutil.FilterOpt{
			Map: func(_ string, st *fstypes.Stat) fsutil.MapResult {
				st.Uid, st.Gid = 0, 0
				return fsutil.MapResultKeep
			},
		})
		if err != nil {
			return nil, err
		}
		dirs[name] = fs
	}
	return dirs, nil
}

// connListener accepts its connection one time, then waits for Close.
type connListener struct {
	conn   chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newConnListener(conn net.Conn) *connListener {
	l := &connListener{conn: make(chan net.Conn, 1), closed: make(chan struct{})}
	l.conn <- conn
	return l
}

func (l *connListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conn:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *connListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *connListener) Addr() net.Addr {
	return &net.UnixAddr{Name: "session", Net: "unix"}
}
