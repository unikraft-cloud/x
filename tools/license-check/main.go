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
	"path/filepath"
	"syscall"

	"github.com/alecthomas/kong"
)

type CLI struct {
	Paths []string `arg:"" name:"path" help:"Files or directories to check." type:"path"`

	Include []string `name:"include" help:"Glob patterns of files to include. May be repeated. Default: all files."`
	Exclude []string `name:"exclude" help:"Glob patterns of files to exclude. May be repeated."`

	NoGitignore bool `name:"no-gitignore" help:"Do not honor .gitignore files."`

	Verbose bool `name:"verbose" short:"v" help:"Print every checked file."`
}

func (c *CLI) Run(_ context.Context) error {
	filter := &fileFilter{
		Include:     c.Include,
		Exclude:     c.Exclude,
		NoGitignore: c.NoGitignore,
	}
	files, err := filter.collect(c.Paths)
	if err != nil {
		return err
	}

	loader := NewFragmentLoader()

	var failed []string
	checked := 0
	for _, p := range files {
		dir := filepath.Dir(p)
		cf, err := loader.Lookup(dir)
		if err != nil {
			return fmt.Errorf("finding license fragment for %s: %w", p, err)
		}
		if cf == nil {
			if c.Verbose {
				fmt.Fprintf(os.Stderr, "skipping: %s\n", p)
			}
			continue
		}

		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("reading %s: %w", p, err)
		}
		checked++
		if !cf.Match(data) {
			failed = append(failed, p)
			continue
		}
		if c.Verbose {
			fmt.Fprintf(os.Stderr, "ok: %s\n", p)
		}
	}

	if len(failed) > 0 {
		fmt.Fprintln(os.Stderr, "files missing required license fragment:")
		for _, p := range failed {
			fmt.Fprintf(os.Stderr, "  %s\n", p)
		}
		return fmt.Errorf("%d/%d file(s) failed license check", len(failed), checked)
	}

	fmt.Fprintf(os.Stderr, "license-check: %d file(s) ok\n", checked)
	return nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cli := &CLI{}
	kctx := kong.Parse(
		cli,
		kong.Name("license-check"),
		kong.Description("Verify that source files contain a required license fragment."),
		kong.BindTo(ctx, (*context.Context)(nil)),
		kong.UsageOnError(),
	)
	if err := kctx.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}
