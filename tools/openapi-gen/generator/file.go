// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package generator

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"unikraft.com/x/tools/openapi-gen/internal/sections"
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
		formatted, err := format.Source(src)
		if err != nil {
			return nil, fmt.Errorf("formatting code: %w", err)
		}
		return formatted, nil
	default:
		return src, nil
	}
}

// File is an in-memory generated file: its clean, slash-separated path
// relative to the module root together with its formatted contents.
type File struct {
	Name string
	Data []byte
}

// render executes the template and returns the resulting in-memory files,
// formatted and with validated names.  A template that produces no output
// yields no files.
func (f *GeneratedFile) render(templates *template.Template) ([]File, error) {
	tmpl := templates.Lookup(f.TemplateName)
	if tmpl == nil {
		return nil, fmt.Errorf("template %s not found", f.TemplateName)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, f.Data); err != nil {
		return nil, fmt.Errorf("executing template: %w", err)
	}
	if len(bytes.TrimSpace(buf.Bytes())) == 0 {
		return nil, nil
	}

	preamble, fileSections, ok, err := sections.Split(buf.Bytes())
	if err != nil {
		return nil, err
	}

	if !ok {
		file, err := formatFile(outputFilename(f.Basename, f.TemplateName), buf.Bytes())
		if err != nil {
			return nil, err
		}
		return []File{file}, nil
	}

	var out []File
	if len(bytes.TrimSpace(preamble)) != 0 {
		file, err := formatFile(outputFilename(f.Basename, f.TemplateName), preamble)
		if err != nil {
			return nil, err
		}
		out = append(out, file)
	}
	for _, section := range fileSections {
		file, err := formatFile(section.Name, section.Content)
		if err != nil {
			return nil, err
		}
		out = append(out, file)
	}
	return out, nil
}

func formatFile(filename string, data []byte) (File, error) {
	formatted, err := formatSource(data, filename)
	if err != nil {
		return File{}, err
	}
	name, err := safeRelName(filename)
	if err != nil {
		return File{}, err
	}
	return File{Name: name, Data: formatted}, nil
}

// safeRelName validates that filename is a clean relative path that cannot
// escape the module root, returning it in slash-separated form.
func safeRelName(filename string) (string, error) {
	clean := filepath.Clean(filename)
	if filepath.IsAbs(clean) {
		return "", fmt.Errorf("filename %q cannot be absolute", filename)
	}
	if clean == "." || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("filename %q cannot escape module root", filename)
	}
	return filepath.ToSlash(clean), nil
}

// cleanGenerated removes previously generated files (any name containing
// ".gen", the marker outputFilename gives every file this tool writes) from
// outputDir, so switching templates or vars doesn't leave stale output
// behind. It's a no-op if outputDir doesn't exist yet.
func cleanGenerated(outputDir string) error {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading output directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.Contains(entry.Name(), ".gen") {
			continue
		}
		if err := os.Remove(filepath.Join(outputDir, entry.Name())); err != nil {
			return fmt.Errorf("removing stale generated file: %w", err)
		}
	}
	return nil
}

func (f File) WriteTo(outputDir string) error {
	name, err := safeRelName(f.Name)
	if err != nil {
		return err
	}
	target := filepath.Join(filepath.Clean(outputDir), filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("creating directory parent: %w", err)
	}
	if err := os.WriteFile(target, f.Data, 0o644); err != nil {
		return fmt.Errorf("writing file: %w", err)
	}
	fmt.Printf("Generated %s\n", name)
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
