// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package buildkit

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serverConfig returns buildkitd's side of mTLS with server's env.
func serverConfig(t *testing.T, server map[string]string) *tls.Config {
	t.Helper()

	cert, err := tls.X509KeyPair([]byte(server[EnvTLSServerCert]), []byte(server[EnvTLSServerKey]))
	require.NoError(t, err)
	cas := x509.NewCertPool()
	require.True(t, cas.AppendCertsFromPEM([]byte(server[EnvTLSCA])))
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    cas,
	}
}

// handshake runs buildkitd's side of mTLS with server's env against client.
func handshake(t *testing.T, server map[string]string, client TLS) error {
	t.Helper()

	srvCfg := serverConfig(t, server)
	cfg, err := client.config()
	require.NoError(t, err)

	sc, cc := net.Pipe()
	defer sc.Close()
	defer cc.Close()
	srv := tls.Server(sc, srvCfg)
	done := make(chan error, 1)
	go func() {
		err := srv.HandshakeContext(t.Context())
		// Unblock the client if the server rejects it.
		sc.Close()
		done <- err
	}()
	cerr := tls.Client(cc, cfg).HandshakeContext(t.Context())
	if err := <-done; err != nil {
		return err
	}
	return cerr
}

func TestGeneratedTLS(t *testing.T) {
	env, err := generateCerts()
	require.NoError(t, err)
	client, err := clientTLS(env)
	require.NoError(t, err)

	assert.NoError(t, handshake(t, env, client))

	other, err := generateCerts()
	require.NoError(t, err)
	stranger, err := clientTLS(other)
	require.NoError(t, err)
	assert.Error(t, handshake(t, env, stranger))
}

func TestClientTLSMasked(t *testing.T) {
	env, err := generateCerts()
	require.NoError(t, err)
	env[EnvTLSClientKey] = "*"

	_, err = clientTLS(env)
	assert.ErrorContains(t, err, EnvTLSClientKey)
}

func TestTLSWriteDir(t *testing.T) {
	env, err := generateCerts()
	require.NoError(t, err)
	client, err := clientTLS(env)
	require.NoError(t, err)
	dir := t.TempDir()
	// An old key that others can read.
	require.NoError(t, os.WriteFile(filepath.Join(dir, keyFile), nil, 0o644))

	require.NoError(t, client.WriteDir(dir))

	ca, err := os.ReadFile(filepath.Join(dir, caFile))
	require.NoError(t, err)
	assert.Equal(t, client.CA, ca)
	_, err = tls.LoadX509KeyPair(filepath.Join(dir, certFile), filepath.Join(dir, keyFile))
	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(dir, keyFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
