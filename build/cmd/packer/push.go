// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/platforms"
	distref "github.com/distribution/reference"
	"github.com/moby/buildkit/session/auth"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/image-spec/contentutil"
	"unikraft.com/x/image-spec/reference"
)

// push makes the image of one platform and pushes its manifest to each TARGET
// tag, then writes the manifest's index entry to OUT.
func push(args []string) error {
	flags := flag.NewFlagSet("push", flag.ContinueOnError)
	runtime := flags.String("runtime", "", "runtime image to take the kernel from")
	config := flags.String("config", "", "file with the image's OCI image JSON")
	socket := flags.String("auth-socket", "", "unix socket of a buildkit auth server for registry credentials")
	var roms, insecureHosts []string
	flags.Func("rom", "a ROM file to add, repeatable", func(s string) error {
		roms = append(roms, s)
		return nil
	})
	flags.Func("insecure-registry", "a registry to reach without verifying its certificate, or over plain HTTP, repeatable", func(s string) error {
		insecureHosts = append(insecureHosts, s)
		return nil
	})
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *runtime == "" || *config == "" || flags.NArg() < 3 {
		return errors.New("usage: packer push --runtime REF --config FILE [flags] INITRD OUT TARGET [TARGET...]")
	}
	initrd, out, targets := flags.Arg(0), flags.Arg(1), flags.Args()[2:]

	var cfg ocispec.Image
	data, err := os.ReadFile(*config)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	if cfg.OS == "" || cfg.Architecture == "" {
		return errors.New("config has no os or architecture")
	}

	ctx := context.Background()
	var authOpts []docker.AuthorizerOpt
	if *socket != "" {
		conn, err := grpc.NewClient("unix://"+*socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return err
		}
		defer conn.Close()
		client := auth.NewAuthClient(conn)
		authOpts = append(authOpts, docker.WithAuthCreds(func(host string) (string, string, error) {
			ctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			resp, err := client.Credentials(ctx, &auth.CredentialsRequest{Host: host})
			if err != nil {
				return "", "", fmt.Errorf("asking the auth socket for %s: %w", host, err)
			}
			return resp.Username, resp.Secret, nil
		}))
	}
	// As docker's insecure registries: TLS without verification, else plain HTTP.
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{InsecureSkipVerify: len(insecureHosts) > 0}
	insecureClient := &http.Client{Transport: docker.NewHTTPFallback(t)}
	authorizer := docker.NewDockerAuthorizer(authOpts...)
	// The token server of an insecure registry is usually on the same host.
	insecureAuthorizer := docker.NewDockerAuthorizer(append(authOpts, docker.WithAuthClient(insecureClient))...)
	// A resolver skips a digest it already pushed, so each target gets its own.
	resolver := func() remotes.Resolver {
		hosts := docker.ConfigureDefaultRegistries(
			docker.WithAuthorizer(authorizer),
			docker.WithPlainHTTP(docker.MatchLocalhost),
		)
		unverified := docker.ConfigureDefaultRegistries(
			docker.WithAuthorizer(insecureAuthorizer),
			docker.WithClient(insecureClient),
		)
		return docker.NewResolver(docker.ResolverOptions{
			Hosts: func(host string) ([]docker.RegistryHost, error) {
				if slices.Contains(insecureHosts, host) {
					return unverified(host)
				}
				return hosts(host)
			},
		})
	}

	named := make([]string, len(targets))
	for i, t := range targets {
		n, err := reference.ParseNormalizedNamed(t)
		if err != nil {
			return fmt.Errorf("parsing target %q: %w", t, err)
		}
		if _, ok := n.(distref.Tagged); !ok {
			return fmt.Errorf("target %q has no tag", t)
		}
		if _, ok := n.(distref.Digested); ok {
			return fmt.Errorf("target %q must not have a digest", t)
		}
		named[i] = n.String()
	}

	loc, err := imagespec.ParseLocationDefault(*runtime)
	if err != nil {
		return fmt.Errorf("parsing runtime reference: %w", err)
	}
	access := imagespec.NewAccessor(imagespec.WithResolver(resolver()))
	res, err := access.Resolve(ctx, loc)
	if err != nil {
		return fmt.Errorf("resolving runtime image: %w", err)
	}
	match := platforms.OnlyStrict(cfg.Platform)
	rt, err := access.Load(ctx, res, match)
	if err != nil {
		return fmt.Errorf("loading runtime image: %w", err)
	}
	defer rt.Close()
	// Load takes a runtime that is one manifest whatever its platform.
	if !match.Match(rt.Image.Platform) {
		return fmt.Errorf("runtime image is %s, not %s", platforms.Format(rt.Image.Platform), platforms.Format(cfg.Platform))
	}

	initrdFile, err := os.Open(initrd)
	if err != nil {
		return err
	}
	opts := []imagespec.NewImageOpt{
		imagespec.WithImageMetadata(imagespec.ImageMetadata{Created: cfg.Created, Author: cfg.Author}),
		imagespec.WithPlatform(cfg.Platform),
		imagespec.WithImageConfig(cfg.Config),
		imagespec.WithKernel(rt.Kernel),
		imagespec.WithInitrd(imagespec.NewOSFile(initrdFile)),
	}
	for _, rom := range roms {
		f, err := os.Open(rom)
		if err != nil {
			return err
		}
		opts = append(opts, imagespec.WithRom(imagespec.NewOSFile(f)))
	}
	img := imagespec.NewImage(opts...)
	rt.Kernel = nil // ownership moves to the image
	defer img.Close()
	// The kernel layer is mounted from the runtime's repository when possible.
	img.Resolved = rt.Resolved

	var desc ocispec.Descriptor
	for _, ref := range named {
		pusher, err := resolver().Pusher(ctx, ref)
		if err != nil {
			return fmt.Errorf("pushing to %s: %w", ref, err)
		}
		if desc, err = imagespec.SaveManifest(ctx, contentutil.FromPusher(pusher), ref, img); err != nil {
			return fmt.Errorf("pushing to %s: %w", ref, err)
		}
	}

	data, err = json.Marshal(desc)
	if err != nil {
		return err
	}
	return os.WriteFile(out, data, 0o644)
}
