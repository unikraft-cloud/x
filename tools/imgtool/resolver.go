// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"net/http"
	"os"

	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/docker/cli/cli/config"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/version"
)

func newAccessor(insecure bool) *imagespec.Accessor {
	headers := http.Header{}
	headers.Set("User-Agent", version.UserAgent())

	dockerConfig := config.LoadDefaultConfigFile(os.Stderr)
	opts := []docker.RegistryOpt{
		docker.WithAuthorizer(docker.NewDockerAuthorizer(docker.WithAuthCreds(func(hostname string) (string, string, error) {
			auth, err := dockerConfig.GetCredentialsStore(hostname).Get(hostname)
			if err != nil {
				return "", "", err
			}
			if auth.IdentityToken != "" {
				return "", auth.IdentityToken, nil
			}
			return auth.Username, auth.Password, nil
		},
		))),
	}
	if insecure {
		opts = append(opts, docker.WithPlainHTTP(docker.MatchAllHosts))
	}

	dro := docker.ResolverOptions{
		Headers: headers,
		Hosts:   docker.ConfigureDefaultRegistries(opts...),
	}
	resolver := docker.NewResolver(dro)
	return imagespec.NewAccessor(
		imagespec.WithResolver(resolver),
		imagespec.WithRegistryHosts(dro.Hosts),
		imagespec.WithRegistryHeaders(dro.Headers),
	)
}
