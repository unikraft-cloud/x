// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package generator

import (
	"fmt"
	"maps"
	"os"
	"runtime"
	"strings"

	"golang.org/x/sync/errgroup"

	"unikraft.com/x/tools/openapi-gen/internal/gitref"
)

// Options configures a single code-generation run.
type Options struct {
	// Input is the path, URL, or Git ref (host/org/repo@ref#file=path) to the
	// OpenAPI spec.
	Input string

	// Output is the directory for generated files.
	Output string

	// Var sets template variables (e.g. "package": "myapi").
	Var map[string]string

	// Templates is the directory or Git ref (host/org/repo@ref#dir=path) with
	// template overrides.
	Templates string

	// Package, when set, filters schemas and operations to this x-package
	// value and is exposed to templates as the "x-package" variable.
	//
	// Deprecated: x-package is emitted only by our proto-based generation
	// pipeline. Prefer Tag and Namespace, which work against any OpenAPI
	// document.
	Package string

	// Tag filters operations to those carrying one of these tags.
	Tag []string

	// Namespace filters models to those in one of these namespaces (e.g.
	// "Instances"). When exactly one namespace is given, its lowercased value
	// is exposed to templates as the "current_package" variable.
	Namespace []string

	// Flatten rewrites namespaced schema names into valid Go identifiers:
	// "strip" drops the namespace prefix, "join" concatenates the segments,
	// and "" leaves them untouched.
	Flatten string
}

// Run generates code from an OpenAPI spec and a set of templates according to
// opts. It is the programmatic equivalent of the openapi-gen command.
func Run(opts Options) error {
	vars := make(map[string]string, len(opts.Var)+2)
	maps.Copy(vars, opts.Var)

	if opts.Package != "" {
		vars["x-package"] = opts.Package
	}

	if len(opts.Namespace) == 1 {
		vars["current_package"] = strings.ToLower(opts.Namespace[0])
	}

	templateDir := opts.Templates
	if g := gitref.Parse(templateDir); g != nil && g.Dir() != "" {
		resolved, cleanup, err := gitref.ResolveDir(g)
		if err != nil {
			return fmt.Errorf("error resolving templates from git: %w", err)
		}
		defer cleanup()
		templateDir = resolved
	}

	generator, err := NewGenerator(opts.Input, vars, templateDir)
	if err != nil {
		return fmt.Errorf("error creating generator: %w", err)
	}

	if opts.Package != "" {
		generator.FilterByPackage(opts.Package)
	}
	if len(opts.Tag) > 0 {
		generator.FilterByTag(opts.Tag)
	}
	if len(opts.Namespace) > 0 {
		generator.FilterByNamespace(opts.Namespace)
	}

	// Flatten namespaced schema names last, after filtering has matched on the
	// original "Namespace.Name" form.
	switch opts.Flatten {
	case "", "strip", "join":
		generator.Flatten(opts.Flatten)
	default:
		return fmt.Errorf("invalid namespace flatten mode %q", opts.Flatten)
	}

	return generator.WriteAll(opts.Output)
}

// Render generates every file in memory and returns them, formatted. It is the
// in-memory counterpart of WriteAll: callers that need the output as data (for
// example, to stream it into an archive) can use Render directly.
func (g *Generator) Render() ([]File, error) {
	specs := g.GenerateAll()

	rendered := make([][]File, len(specs))
	eg := new(errgroup.Group)
	eg.SetLimit(runtime.GOMAXPROCS(0))

	for i := range specs {
		eg.Go(func() error {
			files, err := specs[i].render(g.templates)
			if err != nil {
				return fmt.Errorf("error generating %s: %w", specs[i].Basename, err)
			}
			rendered[i] = files
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	var out []File
	for _, files := range rendered {
		out = append(out, files...)
	}
	return out, nil
}

// WriteAll renders every generated file for g and writes them into outputDir,
// which is created when it does not already exist.
func (g *Generator) WriteAll(outputDir string) error {
	files, err := g.Render()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("error creating output directory: %w", err)
	}

	for _, f := range files {
		if err := f.WriteTo(outputDir); err != nil {
			return err
		}
	}

	return nil
}
