// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"fmt"
	"strings"

	"github.com/alecthomas/kong"
	"unikraft.com/x/kingkong"

	"unikraft.com/x/tools/openapi-gen/generator"
)

type cli struct {
	Input            string            `short:"i" help:"Path, URL, or Git ref (host/org/repo@ref#file=path) to OpenAPI spec." required:""`
	Output           string            `short:"o" help:"Output directory for generated files." required:""`
	Var              map[string]string `short:"v" help:"Set a template variable (e.g. --var package=myapi)." mapsep:","`
	Templates        string            `short:"t" help:"Directory or Git ref (host/org/repo@ref#dir=path) with template overrides." required:""`
	Package          string            `help:"Deprecated: use --tag and --namespace instead. Filter to only include schemas and operations with this x-package value."`
	Tag              []string          `help:"Filter to only include operations carrying one of these tags." sep:"none"`
	Namespace        []string          `help:"Filter to only include schemas in one of these namespaces (e.g. Instances)." sep:"none"`
	NamespacePackage []string          `name:"namespace-package" help:"Namespace whose schemas live in another Go package, as <namespace>[=<package>] (e.g. Org.Common, or Org.Common=shared). The package defaults to the last segment lowercased. Those schemas are not generated here and references to them qualify." sep:"none"`
	Flatten          string            `name:"namespace-flatten" help:"Rewrite namespaced schema names: 'strip' drops the namespace prefix, 'join' concatenates segments." enum:",strip,join" default:""`
}

func main() {
	var cli cli
	ctx := kong.Parse(&cli,
		kong.Name("openapi-gen"),
		kong.Description("Generate code from templates and an OpenAPI spec"),
		kong.Help(kingkong.HelpPrinter("")),
	)

	err := generator.Run(generator.Options{
		Input:            cli.Input,
		Output:           cli.Output,
		Var:              cli.Var,
		Templates:        cli.Templates,
		Package:          cli.Package, //nolint:staticcheck // deprecated, but we still expose it in the cmdline
		Tag:              cli.Tag,
		Namespace:        cli.Namespace,
		NamespacePackage: namespacePackages(cli.NamespacePackage),
		Flatten:          cli.Flatten,
	})
	ctx.FatalIfErrorf(err)

	fmt.Println("Code generation completed successfully!")
}

// namespacePackages parses "<namespace>[=<package>]" values, defaulting the
// package to the namespace's last segment lowercased.
func namespacePackages(values []string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for _, value := range values {
		namespace, pkg, found := strings.Cut(value, "=")
		if !found || pkg == "" {
			pkg = strings.ToLower(namespace[strings.LastIndex(namespace, ".")+1:])
		}
		out[namespace] = pkg
	}
	return out
}
