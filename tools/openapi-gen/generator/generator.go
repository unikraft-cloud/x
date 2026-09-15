// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package generator

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"unikraft.com/x/tools/openapi-gen/internal/openapi"
)

// Generator handles code generation from OpenAPI specs
type Generator struct {
	parser        *openapi.Parser
	templates     *template.Template
	templateNames []string
	vars          map[string]string
	operations    []openapi.PathOperation
	models        []openapi.Model
}

func NewGenerator(specPath string, vars map[string]string, templateDir string) (*Generator, error) {
	parser, err := openapi.NewParser(specPath)
	if err != nil {
		return nil, fmt.Errorf("creating parser: %w", err)
	}

	if err := parser.Preprocess(); err != nil {
		return nil, fmt.Errorf("preprocessing spec: %w", err)
	}

	g := &Generator{
		parser:     parser,
		vars:       vars,
		operations: parser.ParseOperations(),
		models:     parser.ParseModels(),
	}

	tmpl, templateNames, err := loadTemplates(parser, templateDir, &g.models, &g.vars)
	if err != nil {
		return nil, err
	}
	g.templates = tmpl
	g.templateNames = templateNames

	return g, nil
}

// FilterByPackage keeps only operations and models whose package matches pkg.
// Operations match on their x-package extension. Models match on x-package
// when present (our proto-based generation pipeline), else on their
// namespace lowercased (e.g. TypeSpec's "Namespace.Name" schema naming), so a
// document with no x-package extensions at all still filters correctly.
//
// Deprecated: prefer FilterByTag (operations) and FilterByNamespace (models),
// which work against any OpenAPI document without relying on a package name
// happening to equal a lowercased namespace.
func (g *Generator) FilterByPackage(pkg string) {
	var filteredOps []openapi.PathOperation
	for _, op := range g.operations {
		if op.Operation == nil {
			continue
		}
		if opPkg, _ := op.Operation.Extensions["x-package"].(string); opPkg == pkg {
			filteredOps = append(filteredOps, op)
		}
	}
	g.operations = filteredOps

	var filteredModels []openapi.Model
	for _, m := range g.models {
		modelPkg := m.Package
		if modelPkg == "" {
			modelPkg = strings.ToLower(m.Namespace)
		}
		if modelPkg == pkg {
			filteredModels = append(filteredModels, m)
		}
	}
	g.models = filteredModels
}

// FilterByTag keeps only operations carrying at least one of the given tags.
// Models are left untouched (filter them separately via FilterByNamespace).
func (g *Generator) FilterByTag(tags []string) {
	want := make(map[string]bool, len(tags))
	for _, t := range tags {
		want[t] = true
	}
	var filtered []openapi.PathOperation
	for _, op := range g.operations {
		if op.Operation == nil {
			continue
		}
		for _, t := range op.Operation.Tags {
			if want[t] {
				filtered = append(filtered, op)
				break
			}
		}
	}
	g.operations = filtered
}

// FilterByNamespace keeps only models belonging to one of the given
// namespaces, plus any model with no namespace at all: TypeSpec's OpenAPI3
// emitter names a service's own schemas without a namespace prefix and
// reserves the prefix for types pulled in from another namespace, so an
// empty namespace is a signal that a schema is always local, not a reason to
// drop it. Set strict to drop those unnamespaced models too, which gives a
// package that holds an imported namespace and nothing else. Operations are
// left untouched (filter them separately via FilterByTag).
func (g *Generator) FilterByNamespace(namespaces []string, strict bool) {
	want := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		want[ns] = true
	}
	var filtered []openapi.Model
	for _, m := range g.models {
		if want[m.Namespace] || (m.Namespace == "" && !strict) {
			filtered = append(filtered, m)
		}
	}
	g.models = filtered
}

// ExcludeByNamespace drops models belonging to one of the given namespaces.
// A dropped type is no longer generated here, so references to it resolve to
// the Go package its namespace names (see getTypePackage) and one shared
// package can hold a namespace that several documents import.
func (g *Generator) ExcludeByNamespace(namespaces []string) {
	if len(namespaces) == 0 {
		return
	}
	unwanted := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		unwanted[ns] = true
	}
	var filtered []openapi.Model
	for _, m := range g.models {
		if !unwanted[m.Namespace] {
			filtered = append(filtered, m)
		}
	}
	g.models = filtered
}

// Flatten rewrites namespaced schema names (e.g. "Instances.Instance") into
// valid Go identifiers per mode ("strip", "join", or "" for no-op). It runs
// after any namespace/tag filtering so those filters can match on the original
// "Namespace.Name" form.
func (g *Generator) Flatten(mode string) {
	g.parser.Flatten(g.models, mode)
}

func loadTemplates(parser *openapi.Parser, templateDir string, models *[]openapi.Model, vars *map[string]string) (*template.Template, []string, error) {
	if templateDir == "" {
		return nil, nil, fmt.Errorf("template directory not specified")
	}

	files, err := findTemplateFiles(templateDir)
	if err != nil {
		return nil, nil, err
	}

	if len(files) == 0 {
		return nil, nil, fmt.Errorf("no templates found in %s", templateDir)
	}

	tmpl := template.New("").Funcs(openapi.Funcs(parser, models, vars))

	if _, err := tmpl.ParseFiles(files...); err != nil {
		return nil, nil, fmt.Errorf("loading template overrides: %w", err)
	}

	names := make([]string, 0, len(files))
	for _, file := range files {
		names = append(names, filepath.Base(file))
	}

	return tmpl, names, nil
}

func findTemplateFiles(templateDir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(templateDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".tmpl") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking template directory: %w", err)
	}

	sort.Strings(files)
	return files, nil
}

func (g *Generator) GenerateAll() []GeneratedFile {
	files := []GeneratedFile{}

	// Propagate vars to each operation so define blocks can access
	// them via .Var inside templates.
	for i := range g.operations {
		g.operations[i].SetVars(g.vars)
	}

	data := TemplateData{
		vars:       g.vars,
		Operations: g.operations,
		Models:     g.models,
	}

	for _, templateName := range g.templateNames {
		tmpl := g.templates.Lookup(templateName)
		if tmpl == nil {
			continue
		}
		files = append(files, GeneratedFile{TemplateName: templateName, Data: data})
	}

	return files
}

type TemplateData struct {
	vars       map[string]string
	Operations []openapi.PathOperation
	Models     []openapi.Model
}

func (d TemplateData) Var(key, fallback string) string {
	if v, ok := d.vars[key]; ok {
		return v
	}
	return fallback
}
