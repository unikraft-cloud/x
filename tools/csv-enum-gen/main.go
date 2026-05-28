// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// csv-enum-gen turns a CSV file into a generated source file by rendering
// an embedded text/template against the parsed rows and a small JSON config.
//
// The default template emits Go ("fat enum" struct + vars + Values + To<T>),
// but the renderer itself is language-agnostic: swap the template for one
// that emits TypeScript, SQL, etc., and main.go does not change.
//
// Given a CSV like:
//
//	var,country,code,region,city,airport,latitude,longitude
//	IataAAN,ae,aan,Abu Zaby,Al Ain,Al Ain International Airport,24.2617,55.6092
//	...
//
// and a config like:
//
//	{
//	  "package": "iata",
//	  "type":    "Iata",
//	  "key_column":    "var",
//	  "string_column": "code",
//	  "fields": [
//	    {"name": "Country",   "column": "country",   "type": "string"},
//	    ...
//	  ],
//	  "unknown": {"var": "IataUnknown", "values": {...}}
//	}
//
// main.go's job is only to load + validate + sort the data and execute the
// template. All literal formatting, struct shape, switch bodies, etc. live
// in the template via the registered funcs.
package main

import (
	"context"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/alecthomas/kong"
)

//go:embed enum.go.tmpl
var enumTmpl string

// Config is the JSON schema describing how to render a CSV.
type Config struct {
	Package      string   `json:"package"`
	Type         string   `json:"type"`
	Header       string   `json:"header,omitempty"`
	BuildTags    string   `json:"build_tags,omitempty"`
	KeyColumn    string   `json:"key_column"`
	StringColumn string   `json:"string_column"`
	Fields       []Field  `json:"fields"`
	Unknown      *Unknown `json:"unknown,omitempty"`
	Sort         []string `json:"sort,omitempty"`
}

type Field struct {
	Name   string `json:"name"`
	Column string `json:"column"`
	Type   string `json:"type"`
}

type Unknown struct {
	Var    string            `json:"var"`
	Values map[string]string `json:"values"`
}

func (c *Config) validate() error {
	if c.Package == "" {
		return errors.New("config: package is required")
	}
	if c.Type == "" {
		return errors.New("config: type is required")
	}
	if c.KeyColumn == "" {
		return errors.New("config: key_column is required")
	}
	if c.StringColumn == "" {
		return errors.New("config: string_column is required")
	}
	if len(c.Fields) == 0 {
		return errors.New("config: at least one field is required")
	}
	for i, f := range c.Fields {
		if f.Name == "" {
			return fmt.Errorf("config: fields[%d].name is required", i)
		}
		if f.Column == "" {
			return fmt.Errorf("config: fields[%d].column is required", i)
		}
		switch f.Type {
		case "string", "int", "int64", "float64", "bool":
		default:
			return fmt.Errorf("config: fields[%d].type %q is not supported", i, f.Type)
		}
	}
	if c.Unknown != nil && c.Unknown.Var == "" {
		return errors.New("config: unknown.var is required when unknown is set")
	}
	return nil
}

type CLI struct {
	Config string `arg:"" name:"config" help:"Path to JSON config file." type:"existingfile"`
	CSV    string `name:"csv" short:"i" help:"Path to input CSV. Defaults to <config-without-.json>." type:"existingfile"`
	Out    string `name:"out" short:"o" help:"Path to output file." required:"" type:"path"`
}

func (c *CLI) Run(_ context.Context) error {
	cfg, err := loadConfig(c.Config)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	csvPath := c.CSV
	if csvPath == "" {
		csvPath = strings.TrimSuffix(c.Config, ".json")
		if csvPath == c.Config {
			return errors.New("--csv must be set when config path does not end in .json")
		}
	}

	rows, err := loadCSV(csvPath)
	if err != nil {
		return fmt.Errorf("loading csv %s: %w", csvPath, err)
	}
	if len(rows) == 0 {
		return fmt.Errorf("csv %s contains no data rows", csvPath)
	}

	if err := validateRows(cfg, rows); err != nil {
		return err
	}

	if len(cfg.Sort) > 0 {
		sortBy(rows, cfg.Sort)
	}

	sorted := make([]map[string]string, len(rows))
	copy(sorted, rows)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i][cfg.KeyColumn] < sorted[j][cfg.KeyColumn]
	})

	src, err := execute(cfg, rows, sorted)
	if err != nil {
		return fmt.Errorf("rendering: %w", err)
	}

	// gofmt is best-effort; if the template is for a non-Go language it
	// will fail, in which case we just write the raw template output.
	if formatted, ferr := format.Source(src); ferr == nil {
		src = formatted
	}

	if err := os.MkdirAll(filepath.Dir(c.Out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(c.Out, src, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "csv-enum-gen: wrote %d rows to %s\n", len(rows), c.Out)
	return nil
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func loadCSV(path string) ([]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}

	var rows []map[string]string
	for lineno := 2; ; lineno++ {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineno, err)
		}
		if len(record) != len(header) {
			return nil, fmt.Errorf("line %d: got %d columns, want %d",
				lineno, len(record), len(header))
		}
		m := make(map[string]string, len(header))
		for i, col := range header {
			m[col] = record[i]
		}
		rows = append(rows, m)
	}
	return rows, nil
}

func validateRows(cfg *Config, rows []map[string]string) error {
	required := map[string]bool{
		cfg.KeyColumn:    true,
		cfg.StringColumn: true,
	}
	for _, f := range cfg.Fields {
		required[f.Column] = true
	}
	for col := range required {
		if _, ok := rows[0][col]; !ok {
			return fmt.Errorf("csv is missing column %q", col)
		}
	}
	if cfg.Unknown != nil {
		for _, f := range cfg.Fields {
			if _, ok := cfg.Unknown.Values[f.Column]; !ok {
				return fmt.Errorf("unknown.values is missing column %q", f.Column)
			}
		}
	}
	seen := make(map[string]int, len(rows))
	for i, r := range rows {
		k := r[cfg.KeyColumn]
		if k == "" {
			return fmt.Errorf("row %d: empty %s", i+2, cfg.KeyColumn)
		}
		if prev, ok := seen[k]; ok {
			return fmt.Errorf("row %d: duplicate %s=%q (also at row %d)",
				i+2, cfg.KeyColumn, k, prev+2)
		}
		seen[k] = i
	}
	return nil
}

func sortBy(rows []map[string]string, by []string) {
	sort.SliceStable(rows, func(i, j int) bool {
		for _, col := range by {
			if rows[i][col] != rows[j][col] {
				return rows[i][col] < rows[j][col]
			}
		}
		return false
	})
}

// execute parses the embedded template, registers its funcs, and runs it.
func execute(cfg *Config, rows, sorted []map[string]string) ([]byte, error) {
	funcs := template.FuncMap{
		// lit formats a raw CSV string as a Go literal of the named type.
		"lit": func(v, goType string) (string, error) {
			return formatValue(v, goType)
		},
		// field returns the Field that maps to the named CSV column.
		"field": func(c *Config, col string) (Field, error) {
			for _, f := range c.Fields {
				if f.Column == col {
					return f, nil
				}
			}
			return Field{}, fmt.Errorf("no field maps to column %q", col)
		},
		// recv builds a one-character lowercase receiver name from a type.
		"recv": func(t string) string {
			if t == "" {
				return "x"
			}
			return strings.ToLower(t[:1])
		},
	}

	tmpl, err := template.New("enum").Funcs(funcs).Parse(enumTmpl)
	if err != nil {
		return nil, fmt.Errorf("parsing template: %w", err)
	}
	var b strings.Builder
	data := map[string]any{
		"Config":     cfg,
		"Rows":       rows,
		"SortedRows": sorted,
	}
	if err := tmpl.Execute(&b, data); err != nil {
		return nil, fmt.Errorf("executing template: %w", err)
	}
	return []byte(b.String()), nil
}

// formatValue converts a CSV string into a Go literal of the declared type.
func formatValue(v, goType string) (string, error) {
	switch goType {
	case "string":
		return strconv.Quote(v), nil
	case "int", "int64":
		if v == "" {
			return "0", nil
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return "", fmt.Errorf("%q is not a valid %s: %w", v, goType, err)
		}
		return strconv.FormatInt(n, 10), nil
	case "float64":
		if v == "" {
			return "0", nil
		}
		// Emit the original string to preserve precision/formatting.
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			return "", fmt.Errorf("%q is not a valid float64: %w", v, err)
		}
		return v, nil
	case "bool":
		switch strings.ToLower(v) {
		case "", "false", "0", "no":
			return "false", nil
		case "true", "1", "yes":
			return "true", nil
		default:
			return "", fmt.Errorf("%q is not a valid bool", v)
		}
	default:
		return "", fmt.Errorf("unsupported type %q", goType)
	}
}

func main() {
	var cli CLI
	kctx := kong.Parse(&cli,
		kong.Name("csv-enum-gen"),
		kong.Description("Render a CSV through an embedded template."),
		kong.UsageOnError(),
	)
	if err := cli.Run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		kctx.Exit(1)
	}
}
