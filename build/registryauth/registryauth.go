// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package registryauth gives registry credentials to BuildKit, as a session
// attachable, and to containerd's registry resolver, as an authorizer.
package registryauth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/docker/cli/cli/config/configfile"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/session/auth/authprovider"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Credentials gives the username and secret for a registry host, both empty
// for a host it has none for. A secret without a username is a token the
// registry issued.
type Credentials func(host string) (username, secret string, err error)

// registries are the hosts of Unikraft Cloud's registry.
var registries = []string{"unikraft.io", "index.unikraft.io"}

// sessionTimeout is how long a lookup waits for the session, the same as
// buildkit's own resolver waits.
const sessionTimeout = 60 * time.Second

// FromToken returns the credentials that a Unikraft Cloud token gives for
// Unikraft Cloud's registry, index.unikraft.io, and for the given hosts, such
// as a metro's own registry. The token is the user and password in base64,
// like the auth of a docker config.
func FromToken(token string, hosts ...string) (Credentials, error) {
	decoded, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("token is not base64: %w", err)
	}
	username, password, ok := strings.Cut(string(decoded), ":")
	if !ok || username == "" {
		return nil, errors.New("token is not a user and password")
	}

	hosts = slices.Concat(registries, hosts)
	return func(host string) (string, string, error) {
		if slices.Contains(hosts, host) {
			return username, password, nil
		}
		return "", "", nil
	}, nil
}

// FromDockerConfig returns the credentials that cfg has for each host, from
// its auths or its credential helpers.
func FromDockerConfig(cfg *configfile.ConfigFile) Credentials {
	return func(host string) (string, string, error) {
		key := host
		if key == authprovider.DockerHubRegistryHost {
			key = authprovider.DockerHubConfigfileKey
		}
		ac, err := cfg.GetAuthConfig(key)
		if err != nil {
			return "", "", fmt.Errorf("reading docker config for %s: %w", host, err)
		}
		if ac.IdentityToken != "" {
			return "", ac.IdentityToken, nil
		}
		return ac.Username, ac.Password, nil
	}
}

// FromSession returns the credentials that the auth attachable of caller
// gives, as buildkit's own resolver asks for them. A caller without one gives
// none.
func FromSession(ctx context.Context, caller session.Caller) Credentials {
	client := auth.NewAuthClient(caller.Conn())
	return func(host string) (string, string, error) {
		ctx, cancel := context.WithTimeout(ctx, sessionTimeout)
		defer cancel()
		resp, err := client.Credentials(ctx, &auth.CredentialsRequest{Host: host})
		if status.Code(err) == codes.Unimplemented {
			return "", "", nil
		}
		if err != nil {
			return "", "", fmt.Errorf("asking the session for %s credentials: %w", host, err)
		}
		return resp.Username, resp.Secret, nil
	}
}

// Or returns the credentials of c, and those of next for a host that c has
// none for. An error from c stops the lookup.
func (c Credentials) Or(next Credentials) Credentials {
	return func(host string) (string, string, error) {
		username, secret, err := c(host)
		if err != nil || username != "" || secret != "" {
			return username, secret, err
		}
		return next(host)
	}
}

// Authorizer returns an authorizer for containerd's registry resolver that
// logs in to each host with c.
func (c Credentials) Authorizer() docker.Authorizer {
	return docker.NewDockerAuthorizer(docker.WithAuthCreds(c))
}
