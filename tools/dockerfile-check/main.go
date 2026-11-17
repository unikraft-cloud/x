// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"

	"unikraft.com/x/version"
)

type CLI struct {
	Paths []string `arg:"" optional:"" name:"path" help:"Files or directories to check." type:"path" default:"."`

	Allow []string `name:"allow" help:"Prefix of a normalized image reference, such as ghcr.io/acme/, that need not be pinned. May be repeated."`

	NoGitignore bool `name:"no-gitignore" help:"Do not honor .gitignore files."`

	Verbose bool `name:"verbose" short:"v" help:"Print every checked file."`

	Version version.VersionFlag `name:"version" help:"Print version information and quit."`
}

func (c *CLI) Run(_ context.Context) error {
	files, err := collect(c.Paths, !c.NoGitignore)
	if err != nil {
		return err
	}

	failed := 0
	for _, p := range files {
		dt, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("reading %s: %w", p, err)
		}
		images, err := unpinned(dt, c.Allow)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", p, err)
			failed++
			continue
		}
		for _, image := range images {
			fmt.Fprintf(os.Stderr, "%s: %s is not pinned by tag and digest\n", p, image)
		}
		if len(images) > 0 {
			failed++
			continue
		}
		if c.Verbose {
			fmt.Fprintf(os.Stderr, "ok: %s\n", p)
		}
	}

	if failed > 0 {
		return fmt.Errorf("%d/%d file(s) failed dockerfile check", failed, len(files))
	}
	if c.Verbose {
		fmt.Fprintf(os.Stderr, "dockerfile-check: %d file(s) ok\n", len(files))
	}
	return nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cli := &CLI{}
	kctx := kong.Parse(
		cli,
		kong.Name("dockerfile-check"),
		kong.Description("Verify that every image a Dockerfile builds from is pinned by tag and digest."),
		kong.BindTo(ctx, (*context.Context)(nil)),
		kong.UsageOnError(),
	)
	if err := kctx.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}
