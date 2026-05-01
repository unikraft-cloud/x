// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"golang.org/x/tools/imports"
)

// GeneratedFile represents a file to be generated from a template
type GeneratedFile struct {
	TemplateName string
	Data         any
	Basename     string
}

func formatSource(src []byte, filename string) ([]byte, error) {
	switch filepath.Ext(filename) {
	case ".go":
		formatted, err := imports.Process(filename, src, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: goimports failed for %s: %v\n", filename, err)
			fmt.Fprintf(os.Stderr, "Unformatted source:\n%s\n", string(src))
			return nil, fmt.Errorf("formatting code: %w", err)
		}
		return formatted, nil
	default:
		return src, nil
	}
}

func writeGenerated(data []byte, filename, outputDir string) error {
	formatted, err := formatSource(data, filename)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outputDir, filename), formatted, 0o644); err != nil {
		return fmt.Errorf("writing file: %w", err)
	}
	fmt.Printf("Generated %s\n", filename)
	return nil
}

// Generate writes the generated file to the specified directory
func (f *GeneratedFile) Generate(templates *template.Template, outputDir string) error {
	tmpl := templates.Lookup(f.TemplateName)
	if tmpl == nil {
		return fmt.Errorf("template %s not found", f.TemplateName)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, f.Data); err != nil {
		return fmt.Errorf("executing template: %w", err)
	}
	if len(bytes.TrimSpace(buf.Bytes())) == 0 {
		return nil
	}

	preamble, sections, ok, err := splitFileMarkers(buf.Bytes())
	if err != nil {
		return err
	}
	if ok {
		baseFilename := outputFilename(f.Basename, f.TemplateName)
		if len(bytes.TrimSpace(preamble)) != 0 {
			if err := writeGenerated(preamble, baseFilename, outputDir); err != nil {
				return err
			}
		}
		for _, section := range sections {
			if err := writeGenerated(section.content, section.name, outputDir); err != nil {
				return err
			}
		}
		return nil
	}

	filename := outputFilename(f.Basename, f.TemplateName)
	return writeGenerated(buf.Bytes(), filename, outputDir)
}

func outputFilename(basename, templateName string) string {
	filename := basename
	if filename == "" {
		filename = strings.TrimSuffix(templateName, ".tmpl")
	}
	if !strings.Contains(filename, ".gen") {
		if name, ext, ok := strings.Cut(filename, "."); ok {
			filename = name + ".gen." + ext
		} else {
			filename += ".gen"
		}
	}
	return filename
}

// Generator handles code generation from OpenAPI specs
type Generator struct {
	parser        *Parser
	templates     *template.Template
	templateNames []string
	vars          map[string]string
	operations    []PathOperation
	models        []Model
}

func NewGenerator(specPath string, vars map[string]string, templateDir string) (*Generator, error) {
	parser, err := NewParser(specPath)
	if err != nil {
		return nil, fmt.Errorf("creating parser: %w", err)
	}

	Preprocess(parser.doc, parser)

	tmpl, templateNames, err := loadTemplates(parser, templateDir)
	if err != nil {
		return nil, err
	}

	return &Generator{
		parser:        parser,
		templates:     tmpl,
		templateNames: templateNames,
		vars:          vars,
		operations:    parser.ParseOperations(),
		models:        parser.ParseModels(),
	}, nil
}

func loadTemplates(parser *Parser, templateDir string) (*template.Template, []string, error) {
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

	tmpl := template.New("").Funcs(templateFuncs{parser}.Funcs())

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
		g.operations[i].vars = g.vars
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
	Operations []PathOperation
	Models     []Model
}

func (d TemplateData) Var(key, fallback string) string {
	if v, ok := d.vars[key]; ok {
		return v
	}
	return fallback
}
