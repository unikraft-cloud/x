// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/alecthomas/kong"
	"golang.org/x/sync/errgroup"
	"unikraft.com/x/kingkong"
)

type cli struct {
	Input     string            `short:"i" help:"Path, URL, or Git ref (host/org/repo@ref#file=path) to OpenAPI spec." required:""`
	Output    string            `short:"o" help:"Output directory for generated files." required:""`
	Var       map[string]string `short:"v" help:"Set a template variable (e.g. --var package=myapi)." mapsep:","`
	Templates string            `short:"t" help:"Directory or Git ref (host/org/repo@ref#dir=path) with template overrides." required:""`
	Package   string            `help:"Filter to only include schemas and operations with this x-package value."`
	Tag       []string          `help:"Filter to only include operations carrying one of these tags." sep:"none"`
	Namespace []string          `help:"Filter to only include schemas in one of these namespaces (e.g. Instances)." sep:"none"`
	Flatten   string            `name:"namespace-flatten" help:"Rewrite namespaced schema names: 'strip' drops the namespace prefix, 'join' concatenates segments." enum:",strip,join" default:""`
}

func main() {
	var cli cli
	ctx := kong.Parse(&cli,
		kong.Name("openapi-gen"),
		kong.Description("Generate code from templates and an OpenAPI spec"),
		kong.Help(kingkong.HelpPrinter("")),
	)
	err := run(&cli)
	ctx.FatalIfErrorf(err)
}

func run(cli *cli) error {
	vars := cli.Var
	if vars == nil {
		vars = make(map[string]string)
	}

	// Store --package in vars for template access
	if cli.Package != "" {
		vars["x-package"] = cli.Package
	}

	// Expose the current package (lowercased namespace) for templates to
	// distinguish local vs. foreign type references. Derived purely from
	// --namespace so no spec extension is required.
	if len(cli.Namespace) == 1 {
		vars["current_package"] = strings.ToLower(cli.Namespace[0])
	}

	templateDir := cli.Templates
	if g := parseGitRef(templateDir); g != nil && g.dir != "" {
		resolved, cleanup, err := resolveTemplateDirFromGit(g)
		if err != nil {
			return fmt.Errorf("error resolving templates from git: %w", err)
		}
		defer cleanup()
		templateDir = resolved
	}

	generator, err := NewGenerator(cli.Input, vars, templateDir)
	if err != nil {
		return fmt.Errorf("error creating generator: %w", err)
	}

	if cli.Package != "" {
		generator.FilterByPackage(cli.Package)
	}

	if len(cli.Tag) > 0 {
		generator.FilterByTag(cli.Tag)
	}

	if len(cli.Namespace) > 0 {
		generator.FilterByNamespace(cli.Namespace)
	}

	// Flatten namespaced schema names last, after filtering has matched on the
	// original "Namespace.Name" form.
	generator.Flatten(flattenModeFromString(cli.Flatten))

	if err := os.MkdirAll(cli.Output, 0o755); err != nil {
		return fmt.Errorf("error creating output directory: %w", err)
	}

	files := generator.GenerateAll()

	eg := new(errgroup.Group)
	eg.SetLimit(runtime.GOMAXPROCS(0))
	for _, file := range files {
		eg.Go(func() error {
			if err := file.Generate(generator.templates, cli.Output); err != nil {
				return fmt.Errorf("error generating %s: %w", file.Basename, err)
			}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return err
	}

	fmt.Println("Code generation completed successfully!")
	return nil
}
