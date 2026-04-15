// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"bytes"
	"embed"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

func embeddedTemplateNames() ([]string, error) {
	files, err := fs.Glob(templatesFS, "templates/*.tmpl")
	if err != nil {
		return nil, fmt.Errorf("finding embedded templates: %w", err)
	}

	names := make([]string, 0, len(files))
	for _, file := range files {
		names = append(names, filepath.Base(file))
	}
	return names, nil
}

// GeneratedFile represents a file to be generated from a template
type GeneratedFile struct {
	TemplateName string
	Data         any
	Basename     string
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
		// skip if template produced no output
		return nil
	}

	preamble, sections, ok, err := splitFileMarkers(buf.Bytes())
	if err != nil {
		return err
	}
	if ok {
		baseFilename := outputFilename(f.Basename, f.TemplateName)
		if len(bytes.TrimSpace(preamble)) != 0 {
			formatted, err := format.Source(preamble)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: gofmt failed for %s: %v\n", baseFilename, err)
				fmt.Fprintf(os.Stderr, "Unformatted source:\n%s\n", string(preamble))
				return fmt.Errorf("formatting code: %w", err)
			}
			if err := os.WriteFile(filepath.Join(outputDir, baseFilename), formatted, 0o644); err != nil {
				return fmt.Errorf("writing file: %w", err)
			}
			fmt.Printf("Generated %s\n", baseFilename)
		}
		for _, section := range sections {
			formatted, err := format.Source(section.content)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: gofmt failed for %s: %v\n", section.name, err)
				fmt.Fprintf(os.Stderr, "Unformatted source:\n%s\n", string(section.content))
				return fmt.Errorf("formatting code: %w", err)
			}
			filename := applyVariantToFilename(baseFilename, section.name)
			if err := os.WriteFile(filepath.Join(outputDir, filename), formatted, 0o644); err != nil {
				return fmt.Errorf("writing file: %w", err)
			}
			fmt.Printf("Generated %s\n", filename)
		}
		return nil
	}

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: gofmt failed for %s: %v\n", f.TemplateName, err)
		fmt.Fprintf(os.Stderr, "Unformatted source:\n%s\n", buf.String())
		return fmt.Errorf("formatting code: %w", err)
	}

	filename := outputFilename(f.Basename, f.TemplateName)

	if err := os.WriteFile(filepath.Join(outputDir, filename), formatted, 0o644); err != nil {
		return fmt.Errorf("writing file: %w", err)
	}

	fmt.Printf("Generated %s\n", filename)
	return nil
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
	tmpl := template.New("").Funcs(templateFuncs{parser}.Funcs())

	if templateDir != "" {
		files, err := findTemplateFiles(templateDir)
		if err != nil {
			return nil, nil, err
		}
		if len(files) == 0 {
			return nil, nil, fmt.Errorf("no templates found in %s", templateDir)
		}
		if _, err := tmpl.ParseFiles(files...); err != nil {
			return nil, nil, fmt.Errorf("loading template overrides: %w", err)
		}
		names := make([]string, 0, len(files))
		for _, file := range files {
			names = append(names, filepath.Base(file))
		}
		return tmpl, names, nil
	}

	if _, err := tmpl.ParseFS(templatesFS, "templates/*.tmpl"); err != nil {
		return nil, nil, fmt.Errorf("loading templates: %w", err)
	}

	names, err := embeddedTemplateNames()
	if err != nil {
		return nil, nil, err
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
