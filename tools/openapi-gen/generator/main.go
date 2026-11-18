// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package generator

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/alecthomas/kong"
	"unikraft.com/x/kingkong"
)

type cli struct {
	Input     string            `short:"i" help:"Path, URL, or Git ref (host/org/repo@ref#file=path) to OpenAPI spec." required:""`
	Output    string            `short:"o" help:"Output directory for generated files." required:""`
	Var       map[string]string `short:"v" help:"Set a template variable (e.g. --var package=myapi)." mapsep:","`
	Templates string            `short:"t" help:"Template pack (${packs}), or a directory or Git ref (host/org/repo@ref#dir=path) with templates." required:""`
	Package   string            `help:"Deprecated: use --tag and --namespace instead. Filter to only include schemas and operations with this x-package value."`
	Tag       []string          `help:"Filter to only include operations carrying one of these tags." sep:"none"`
	Namespace []string          `help:"Filter to only include schemas in one of these namespaces (e.g. Instances)." sep:"none"`
	Flatten   string            `name:"namespace-flatten" help:"Rewrite namespaced schema names: 'strip' drops the namespace prefix, 'join' concatenates segments." enum:",strip,join" default:""`
}

// Main runs the openapi-gen command line, with --templates able to name any
// of packs. It lets a repo with its own packs build its own openapi-gen.
func Main(packs map[string]*Pack) {
	var cli cli
	ctx := kong.Parse(&cli,
		kong.Name("openapi-gen"),
		kong.Description("Generate code from templates and an OpenAPI spec"),
		kong.Help(kingkong.HelpPrinter("")),
		kong.Vars{"packs": strings.Join(slices.Sorted(maps.Keys(packs)), ", ")},
	)

	err := Run(Options{
		Input:     cli.Input,
		Output:    cli.Output,
		Var:       cli.Var,
		Templates: cli.Templates,
		Packs:     packs,
		Package:   cli.Package,
		Tag:       cli.Tag,
		Namespace: cli.Namespace,
		Flatten:   cli.Flatten,
	})
	ctx.FatalIfErrorf(err)

	fmt.Println("Code generation completed successfully!")
}
