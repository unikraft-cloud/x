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

	"github.com/ettle/strcase"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

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

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: gofmt failed for %s: %v\n", f.TemplateName, err)
		fmt.Fprintf(os.Stderr, "Unformatted source:\n%s\n", buf.String())
		return fmt.Errorf("formatting code: %w", err)
	}

	filename := f.Basename
	if filename == "" {
		filename = strings.TrimSuffix(f.TemplateName, ".tmpl")
	}
	if !strings.Contains(filename, ".gen") {
		if name, ext, ok := strings.Cut(filename, "."); ok {
			filename = name + ".gen." + ext
		} else {
			filename += ".gen"
		}
	}

	if err := os.WriteFile(filepath.Join(outputDir, filename), formatted, 0o644); err != nil {
		return fmt.Errorf("writing file: %w", err)
	}

	fmt.Printf("Generated %s\n", filename)
	return nil
}

// Generator handles code generation from OpenAPI specs
type Generator struct {
	parser    *Parser
	templates *template.Template
}

// NewGenerator creates a new code generator
func NewGenerator(specPath, packageName, templateDir string) (*Generator, error) {
	parser, err := NewParser(specPath, packageName)
	if err != nil {
		return nil, fmt.Errorf("creating parser: %w", err)
	}

	Preprocess(parser.doc, parser)

	tmpl, err := loadTemplates(parser, templateDir)
	if err != nil {
		return nil, err
	}

	return &Generator{parser: parser, templates: tmpl}, nil
}

func loadTemplates(parser *Parser, templateDir string) (*template.Template, error) {
	tmpl := template.New("").
		Funcs(templateFuncs{parser}.Funcs())

	if templateDir == "" {
		return tmpl.ParseFS(templatesFS, "templates/*.tmpl")
	}

	files, err := findTemplateFiles(templateDir)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no templates found in %s", templateDir)
	}
	return tmpl.ParseFiles(files...)
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

func (g *Generator) GenerateModels() []GeneratedFile {
	modelFiles := g.parser.ParseModels()

	files := make([]GeneratedFile, 0, len(modelFiles))
	for _, mf := range modelFiles {
		files = append(files, GeneratedFile{
			TemplateName: "model.go.tmpl",
			Data: map[string]any{
				"PackageName": g.parser.packageName,
				"SchemaName":  mf.SchemaName,
				"Schema":      mf.Schema,
			},
			Basename: "model_" + strcase.ToSnake(mf.SchemaName) + ".gen.go",
		})
	}
	return files
}

func (g *Generator) GenerateClient() GeneratedFile {
	operations := g.parser.ParseOperations()

	data := g.packageData()
	data["Operations"] = operations
	return GeneratedFile{TemplateName: "client.go.tmpl", Data: data}
}

func (g *Generator) GenerateClientMethodOpts() GeneratedFile {
	operations := g.parser.ParseOperations()

	data := g.packageData()
	data["Operations"] = operations
	return GeneratedFile{TemplateName: "client_method_opts.go.tmpl", Data: data}
}

func (g *Generator) GenerateRequest() GeneratedFile {
	return GeneratedFile{TemplateName: "request.go.tmpl", Data: g.packageData()}
}

func (g *Generator) GenerateResponse() GeneratedFile {
	return GeneratedFile{TemplateName: "response.go.tmpl", Data: g.packageData()}
}

func (g *Generator) GenerateClientOptions() GeneratedFile {
	return GeneratedFile{TemplateName: "client_options.go.tmpl", Data: g.packageData()}
}

func (g *Generator) GenerateHTTPAPIErrors() GeneratedFile {
	return GeneratedFile{TemplateName: "http_api_errors.go.tmpl", Data: g.packageData()}
}

func (g *Generator) packageData() map[string]any {
	return map[string]any{"PackageName": g.parser.packageName}
}
