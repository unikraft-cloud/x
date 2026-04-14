// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"fmt"
	"os"

	"github.com/alecthomas/kong"
	"unikraft.com/x/kingkong"
)

type cli struct {
	Input     string            `short:"i" help:"Path, URL, or Git ref (host/org/repo@ref#file=path) to OpenAPI spec." required:""`
	Output    string            `short:"o" help:"Output directory for generated files." required:""`
	Var       map[string]string `short:"v" help:"Set a template variable (e.g. --var package=myapi)." mapsep:","`
	Templates string            `short:"t" help:"Directory or Git ref (host/org/repo@ref#dir=path) with template overrides." required:""`
}

func main() {
	var cli cli
	ctx := kong.Parse(&cli,
		kong.Name("codegen"),
		kong.Description("Generate Go SDK code from OpenAPI spec"),
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

	if err := os.MkdirAll(cli.Output, 0o755); err != nil {
		return fmt.Errorf("error creating output directory: %w", err)
	}

	files := generator.GenerateAll()

	for _, file := range files {
		if err := file.Generate(generator.templates, cli.Output); err != nil {
			return fmt.Errorf("error generating %s: %w", file.Basename, err)
		}
	}

	fmt.Println("Code generation completed successfully!")
	return nil
}
