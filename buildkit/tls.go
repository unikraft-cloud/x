// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package buildkit

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const serverName = "buildkitd"

// Names of the files that WriteDir writes, which buildctl --tlsdir reads.
const (
	caFile   = "ca.pem"
	certFile = "cert.pem"
	keyFile  = "key.pem"
)

// generateCerts creates a throwaway CA, signs a server and a client
// certificate with it, drops the CA key, and returns the PEMs as instance env.
func generateCerts() (map[string]string, error) {
	now := time.Now()
	template := func(cn string) *x509.Certificate {
		return &x509.Certificate{
			Subject:   pkix.Name{CommonName: cn},
			NotBefore: now.Add(-time.Hour),
			// Rotating means recreating the builder.
			NotAfter: now.AddDate(10, 0, 0),
		}
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	caTmpl := template("buildkit-ca")
	caTmpl.IsCA = true
	caTmpl.BasicConstraintsValid = true
	caTmpl.MaxPathLenZero = true
	caTmpl.KeyUsage = x509.KeyUsageCertSign
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}

	env := map[string]string{EnvTLSCA: encode("CERTIFICATE", caDER)}
	leaf := func(certEnv, keyEnv, cn string, usage x509.ExtKeyUsage, dns ...string) error {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return err
		}
		tmpl := template(cn)
		tmpl.KeyUsage = x509.KeyUsageDigitalSignature
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{usage}
		tmpl.DNSNames = dns
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
		if err != nil {
			return err
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return err
		}
		env[certEnv] = encode("CERTIFICATE", der)
		env[keyEnv] = encode("PRIVATE KEY", keyDER)
		return nil
	}
	if err := leaf(EnvTLSServerCert, EnvTLSServerKey, serverName, x509.ExtKeyUsageServerAuth, serverName); err != nil {
		return nil, err
	}
	if err := leaf(EnvTLSClientCert, EnvTLSClientKey, "buildkit-client", x509.ExtKeyUsageClientAuth); err != nil {
		return nil, err
	}
	return env, nil
}

func encode(typ string, der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}))
}

// TLS holds the PEM-encoded CA, client certificate and client key for a
// builder's mTLS.
type TLS struct {
	ServerName    string
	CA, Cert, Key []byte
}

// clientTLS reads the CA and the client certificate and key back from a
// builder's env.
func clientTLS(env map[string]string) (TLS, error) {
	for _, k := range []string{EnvTLSCA, EnvTLSClientCert, EnvTLSClientKey} {
		if v := env[k]; v == "" || v == "*" {
			return TLS{}, fmt.Errorf("%s is missing or masked", k)
		}
	}
	return TLS{
		ServerName: serverName,
		CA:         []byte(env[EnvTLSCA]),
		Cert:       []byte(env[EnvTLSClientCert]),
		Key:        []byte(env[EnvTLSClientKey]),
	}, nil
}

// WriteDir writes the CA, certificate and key to dir, with the file names that
// buildctl --tlsdir reads.
func (t TLS) WriteDir(dir string) error {
	if len(t.CA) == 0 {
		return errors.New("no TLS to write")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for name, data := range map[string][]byte{caFile: t.CA, certFile: t.Cert, keyFile: t.Key} {
		if err := writePrivate(filepath.Join(dir, name), data); err != nil {
			return err
		}
	}
	return nil
}

// writePrivate writes data to a new file that only the owner can read, then
// moves it to path, so that the mode of an old file at path does not apply.
func writePrivate(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// config returns the client side of buildkit's mTLS.
func (t TLS) config() (*tls.Config, error) {
	cert, err := tls.X509KeyPair(t.Cert, t.Key)
	if err != nil {
		return nil, fmt.Errorf("loading client certificate: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(t.CA) {
		return nil, errors.New("no CA certificate found")
	}
	return &tls.Config{
		RootCAs:      roots,
		Certificates: []tls.Certificate{cert},
		ServerName:   t.ServerName,
		MinVersion:   tls.VersionTLS13,
	}, nil
}
