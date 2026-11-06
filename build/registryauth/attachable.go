// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package registryauth

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"time"

	authutil "github.com/containerd/containerd/v2/core/remotes/docker/auth"
	remoteserrors "github.com/containerd/containerd/v2/core/remotes/errors"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth"
	"golang.org/x/crypto/nacl/sign"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// defaultExpiration is how long a token lasts when its registry does not
// say, in seconds, the same as buildkit's own auth provider assumes.
const defaultExpiration = 60

// Option configures an Attachable.
type Option func(*attachable)

// WithHTTPClient fetches the tokens through c instead of http.DefaultClient.
// The daemon names the realm of each token, so c can keep that in bounds.
func WithHTTPClient(c *http.Client) Option {
	return func(a *attachable) { a.client = c }
}

// Attachable returns a session attachable that answers BuildKit's requests
// for registry credentials with c, as buildkit's own auth provider does from
// the docker config. For a host that c has credentials for, the daemon has
// the attachable fetch the bearer tokens itself, so the secret need not
// cross the session for a registry that issues them. The daemon can still
// ask for the credentials, and does for basic auth, so it must be trusted
// with them.
func (c Credentials) Attachable(opts ...Option) session.Attachable {
	a := &attachable{
		creds:  c,
		seed:   []byte(rand.Text()),
		client: http.DefaultClient,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

type attachable struct {
	creds  Credentials
	seed   []byte
	client *http.Client
}

// Register implements session.Attachable.
func (a *attachable) Register(server *grpc.Server) {
	auth.RegisterAuthServer(server, a)
}

// Credentials implements auth.AuthServer.
func (a *attachable) Credentials(_ context.Context, req *auth.CredentialsRequest) (*auth.CredentialsResponse, error) {
	username, secret, err := a.creds(req.Host)
	if err != nil {
		return nil, err
	}
	return &auth.CredentialsResponse{Username: username, Secret: secret}, nil
}

// FetchToken implements auth.AuthServer. The daemon calls it for the hosts
// that GetTokenAuthority answered for, which are those with credentials.
func (a *attachable) FetchToken(ctx context.Context, req *auth.FetchTokenRequest) (*auth.FetchTokenResponse, error) {
	username, secret, err := a.creds(req.Host)
	if err != nil {
		return nil, err
	}
	if secret == "" {
		return nil, status.Errorf(codes.Unavailable, "no credentials for %s", req.Host)
	}
	to := authutil.TokenOptions{
		Realm:    req.Realm,
		Service:  req.Service,
		Scopes:   req.Scopes,
		Username: username,
		Secret:   secret,
	}

	resp, err := authutil.FetchTokenWithOAuth(ctx, a.client, nil, cmp.Or(req.ClientID, "buildkit-client"), to)
	if err != nil {
		var unexpected remoteserrors.ErrUnexpectedStatus
		if errors.As(err, &unexpected) && (unexpected.StatusCode == http.StatusMethodNotAllowed ||
			unexpected.StatusCode == http.StatusNotFound || unexpected.StatusCode == http.StatusUnauthorized) {
			resp, err := authutil.FetchToken(ctx, a.client, nil, to)
			if err != nil {
				return nil, fmt.Errorf("fetching token for %s: %w", req.Host, err)
			}
			return tokenResponse(resp.Token, resp.IssuedAt, resp.ExpiresInSeconds), nil
		}
		return nil, fmt.Errorf("fetching oauth token for %s: %w", req.Host, err)
	}
	return tokenResponse(resp.AccessToken, resp.IssuedAt, resp.ExpiresInSeconds), nil
}

// GetTokenAuthority implements auth.AuthServer. It gives the public key that
// signs the token requests for a host with credentials. For any other host
// it is unavailable, and the daemon fetches anonymous tokens itself.
func (a *attachable) GetTokenAuthority(_ context.Context, req *auth.GetTokenAuthorityRequest) (*auth.GetTokenAuthorityResponse, error) {
	key, err := a.authorityKey(req.Host, req.Salt)
	if err != nil {
		return nil, err
	}
	return &auth.GetTokenAuthorityResponse{PublicKey: key.Public().(ed25519.PublicKey)}, nil
}

// VerifyTokenAuthority implements auth.AuthServer.
func (a *attachable) VerifyTokenAuthority(_ context.Context, req *auth.VerifyTokenAuthorityRequest) (*auth.VerifyTokenAuthorityResponse, error) {
	key, err := a.authorityKey(req.Host, req.Salt)
	if err != nil {
		return nil, err
	}
	priv := new([ed25519.PrivateKeySize]byte)
	copy(priv[:], key)
	return &auth.VerifyTokenAuthorityResponse{Signed: sign.Sign(nil, req.Payload, priv)}, nil
}

// authorityKey derives the key that signs the token requests for host from
// the daemon's salt and the attachable's seed.
func (a *attachable) authorityKey(host string, salt []byte) (ed25519.PrivateKey, error) {
	_, secret, err := a.creds(host)
	if err != nil {
		return nil, err
	}
	if secret == "" {
		return nil, status.Errorf(codes.Unavailable, "no credentials for %s", host)
	}
	mac := hmac.New(sha256.New, salt)
	mac.Write(a.seed)
	return ed25519.NewKeyFromSeed(mac.Sum(nil)[:ed25519.SeedSize]), nil
}

func tokenResponse(token string, issuedAt time.Time, expires int) *auth.FetchTokenResponse {
	if expires == 0 {
		expires = defaultExpiration
	}
	resp := &auth.FetchTokenResponse{Token: token, ExpiresIn: int64(expires)}
	if !issuedAt.IsZero() {
		resp.IssuedAt = issuedAt.Unix()
	}
	return resp
}
