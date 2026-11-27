// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package registryauth_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/cli/cli/config/configfile"
	"github.com/docker/cli/cli/config/types"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/session/auth/authprovider"
	"github.com/moby/buildkit/session/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/build/registryauth"
)

// token is a Unikraft Cloud token, "alice:s3cret" in base64.
const token = "YWxpY2U6czNjcmV0"

func TestFromToken(t *testing.T) {
	creds, err := registryauth.FromToken(token, "index.fra.unikraft.cloud")
	require.NoError(t, err)

	for _, tc := range []struct {
		name, host, user, secret string
	}{
		{"the registry gets the user and password", "index.unikraft.io", "alice", "s3cret"},
		{"so does its short name", "unikraft.io", "alice", "s3cret"},
		{"so does a host given with the token", "index.fra.unikraft.cloud", "alice", "s3cret"},
		{"another registry gets nothing", "ghcr.io", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user, secret, err := creds(tc.host)
			require.NoError(t, err)
			assert.Equal(t, tc.user, user)
			assert.Equal(t, tc.secret, secret)
		})
	}
}

func TestFromTokenMalformed(t *testing.T) {
	for _, tc := range []struct {
		name, token string
	}{
		{"not base64", "not base64!"},
		{"no separator", "czNjcmV0"},   // "s3cret"
		{"empty user", "OnMzY3JldA=="}, // ":s3cret"
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := registryauth.FromToken(tc.token)
			assert.Error(t, err)
		})
	}
}

// dockerConfig is a docker config with a password for ghcr.io and Docker Hub,
// and an identity token for quay.io.
func dockerConfig() *configfile.ConfigFile {
	cfg := configfile.New("")
	cfg.AuthConfigs = map[string]types.AuthConfig{
		"ghcr.io":                     {Username: "alice", Password: "s3cret"},
		"https://index.docker.io/v1/": {Username: "bob", Password: "hunter2"},
		"quay.io":                     {Username: "carol", IdentityToken: "1d-t0k3n"},
	}
	return cfg
}

func TestFromDockerConfig(t *testing.T) {
	creds := registryauth.FromDockerConfig(dockerConfig())

	for _, tc := range []struct {
		name, host, user, secret string
	}{
		{"a registry gets its user and password", "ghcr.io", "alice", "s3cret"},
		{"Docker Hub gets the entry of its config key", "registry-1.docker.io", "bob", "hunter2"},
		{"an identity token is a secret without a user", "quay.io", "", "1d-t0k3n"},
		{"another registry gets nothing", "index.unikraft.io", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user, secret, err := creds(tc.host)
			require.NoError(t, err)
			assert.Equal(t, tc.user, user)
			assert.Equal(t, tc.secret, secret)
		})
	}
}

// caller returns the caller of the session in g.
func caller(t *testing.T, sm *session.Manager, g session.Group) session.Caller {
	t.Helper()

	var c session.Caller
	require.NoError(t, sm.Any(t.Context(), g, func(_ context.Context, _ string, sc session.Caller) error {
		c = sc
		return nil
	}))
	return c
}

func TestFromSession(t *testing.T) {
	t.Run("asks the auth attachable of the client", func(t *testing.T) {
		sm, g := daemon(t, authprovider.NewDockerAuthProvider(authprovider.DockerAuthProviderConfig{
			AuthConfigProvider: authprovider.LoadAuthConfig(dockerConfig()),
		}))
		user, secret, err := registryauth.FromSession(t.Context(), caller(t, sm, g))("ghcr.io")
		require.NoError(t, err)
		assert.Equal(t, "alice", user)
		assert.Equal(t, "s3cret", secret)
	})

	t.Run("gives nothing for a client without auth", func(t *testing.T) {
		sm, g := daemon(t)
		user, secret, err := registryauth.FromSession(t.Context(), caller(t, sm, g))("ghcr.io")
		require.NoError(t, err)
		assert.Empty(t, user)
		assert.Empty(t, secret)
	})
}

func TestOr(t *testing.T) {
	static := func(user, secret string) registryauth.Credentials {
		return func(string) (string, string, error) { return user, secret, nil }
	}
	failing := registryauth.Credentials(func(string) (string, string, error) {
		return "", "", errors.New("config is broken")
	})

	for _, tc := range []struct {
		name          string
		first, next   registryauth.Credentials
		user, secret  string
		expectFailure bool
	}{
		{"the first credentials win", static("alice", "s3cret"), failing, "alice", "s3cret", false},
		{"a token without a user counts", static("", "t0k3n"), failing, "", "t0k3n", false},
		{"none falls through to the next", static("", ""), static("bob", "hunter2"), "bob", "hunter2", false},
		{"an error stops the lookup", failing, static("bob", "hunter2"), "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user, secret, err := tc.first.Or(tc.next)("ghcr.io")
			if tc.expectFailure {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.user, user)
			assert.Equal(t, tc.secret, secret)
		})
	}
}

// daemon opens a session that serves attachables to a session manager, as
// the client does to buildkitd, and returns the manager and the session's group.
func daemon(t *testing.T, attachables ...session.Attachable) (*session.Manager, session.Group) {
	t.Helper()

	sm, err := session.NewManager()
	require.NoError(t, err)
	s, err := session.NewSession(t.Context(), "")
	require.NoError(t, err)
	for _, a := range attachables {
		s.Allow(a)
	}
	t.Cleanup(func() { _ = s.Close() })

	dialer := testutil.TestStream(func(ctx context.Context, conn net.Conn, meta map[string][]string) error {
		return sm.HandleConn(ctx, conn, meta)
	})
	go func() { _ = s.Run(t.Context(), session.Dialer(dialer)) }()

	return sm, session.NewGroup(s.ID())
}

// startRealm serves a token endpoint that issues a token to alice with her
// password, and returns its URL. With oauth it takes the OAuth POST as well
// as the GET, without it only the GET, like a Harbor.
func startRealm(t *testing.T, oauth bool) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var user, pass string
		switch {
		case r.Method == http.MethodPost && oauth:
			user, pass = r.FormValue("username"), r.FormValue("password")
		case r.Method == http.MethodGet:
			user, pass, _ = r.BasicAuth()
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if user != "alice" || pass != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		assert.Equal(t, "registry", r.FormValue("service"))
		assert.Equal(t, "repository:acme/app:push", r.FormValue("scope"))
		if r.Method == http.MethodPost {
			_, _ = fmt.Fprint(w, `{"access_token":"0auth","expires_in":300}`)
		} else {
			_, _ = fmt.Fprint(w, `{"token":"t0k3n","expires_in":300}`)
		}
	}))
	t.Cleanup(srv.Close)

	return srv.URL + "/token"
}

func TestAttachable(t *testing.T) {
	creds, err := registryauth.FromToken(token)
	require.NoError(t, err)
	sm, g := daemon(t, creds.Attachable())
	ctx := t.Context()

	t.Run("shares the credentials of the registry", func(t *testing.T) {
		_, user, secret, err := auth.CredentialsFunc(ctx, sm, g)("index.unikraft.io")
		require.NoError(t, err)
		assert.Equal(t, "alice", user)
		assert.Equal(t, "s3cret", secret)
	})

	t.Run("leaves other registries to the daemon", func(t *testing.T) {
		_, pubKey, err := auth.GetTokenAuthority(ctx, "ghcr.io", sm, g)
		require.NoError(t, err)
		assert.Nil(t, pubKey, "the daemon fetches anonymous tokens itself")
	})

	t.Run("fetches the registry's tokens itself", func(t *testing.T) {
		_, pubKey, err := auth.GetTokenAuthority(ctx, "index.unikraft.io", sm, g)
		require.NoError(t, err)
		require.NotNil(t, pubKey, "the attachable signs token requests")
		_, ok, err := auth.VerifyTokenAuthority(ctx, "index.unikraft.io", pubKey, sm, g)
		require.NoError(t, err)
		assert.True(t, ok)

		for _, tc := range []struct {
			name  string
			oauth bool
			token string
		}{
			{"from a realm with the OAuth endpoint", true, "0auth"},
			{"from one without it, like Harbor", false, "t0k3n"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				resp, err := auth.FetchToken(ctx, &auth.FetchTokenRequest{
					ClientID: "buildkit-client",
					Host:     "index.unikraft.io",
					Realm:    startRealm(t, tc.oauth),
					Service:  "registry",
					Scopes:   []string{"repository:acme/app:push"},
				}, sm, g)
				require.NoError(t, err)
				assert.Equal(t, tc.token, resp.Token)
				assert.EqualValues(t, 300, resp.ExpiresIn)
			})
		}
	})

	t.Run("fetches the tokens through the client it is given", func(t *testing.T) {
		var realm string
		client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			realm = r.URL.String()
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"fr0m-cl1ent","expires_in":300}`)),
				Request:    r,
			}, nil
		})}
		sm, g := daemon(t, creds.Attachable(registryauth.WithHTTPClient(client)))

		// Nothing answers at the realm: only the client given can produce a token.
		resp, err := auth.FetchToken(ctx, &auth.FetchTokenRequest{
			ClientID: "buildkit-client",
			Host:     "index.unikraft.io",
			Realm:    "http://realm.invalid/token",
			Service:  "registry",
			Scopes:   []string{"repository:acme/app:push"},
		}, sm, g)
		require.NoError(t, err)
		assert.Equal(t, "fr0m-cl1ent", resp.Token)
		assert.Equal(t, "http://realm.invalid/token", realm)
	})
}

// roundTripperFunc is an http.RoundTripper made of a function.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
