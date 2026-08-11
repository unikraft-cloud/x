// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package openapi

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"unikraft.com/x/tools/openapi-gen/internal/gitref"
)

// Parser handles parsing OpenAPI specs into our template data structures
type Parser struct {
	doc *openapi3.T
}

// Model represents a single model file to be generated
type Model struct {
	SchemaName string
	Schema     *openapi3.Schema
	// Package is derived from the x-package extension.
	//
	// Deprecated: x-package is only emitted by our proto-based generation
	// pipeline. Prefer Namespace, which works for any OpenAPI document.
	Package string
	// Namespace is derived from the "Namespace.Name" schema name prefix
	// (e.g. as emitted by platform-api's TypeSpec compiler).
	Namespace string
}

// isURL reports whether s looks like an HTTP(S) URL.
func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// readSpec reads the OpenAPI specification bytes from a local file path or
// an HTTP(S) URL.
func readSpec(input string) ([]byte, error) {
	if isURL(input) {
		resp, err := http.Get(input) //nolint:gosec,noctx // URL is a trusted CLI-provided spec path; no request context needed for this one-shot generator
		if err != nil {
			return nil, fmt.Errorf("fetching spec from URL: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetching spec from URL: HTTP %d", resp.StatusCode)
		}
		return io.ReadAll(resp.Body)
	}
	if g := gitref.Parse(input); g != nil {
		return gitref.ReadSpec(g)
	}
	return os.ReadFile(input)
}

// NewParser creates a new OpenAPI parser.  input may be a local file path
// or an HTTP(S) URL.
func NewParser(input string) (*Parser, error) {
	data, err := readSpec(input)
	if err != nil {
		return nil, fmt.Errorf("reading OpenAPI spec: %w", err)
	}

	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true

	var doc *openapi3.T
	switch {
	case isURL(input):
		u, err := url.Parse(input)
		if err != nil {
			return nil, fmt.Errorf("parsing spec URL: %w", err)
		}
		doc, err = loader.LoadFromDataWithPath(data, u)
		if err != nil {
			return nil, fmt.Errorf("loading OpenAPI spec: %w", err)
		}
	case gitref.Parse(input) != nil:
		doc, err = loader.LoadFromData(data)
		if err != nil {
			return nil, fmt.Errorf("loading OpenAPI spec: %w", err)
		}
	default:
		doc, err = loader.LoadFromFile(input)
		if err != nil {
			return nil, fmt.Errorf("loading OpenAPI spec: %w", err)
		}
	}

	return &Parser{
		doc: doc,
	}, nil
}

// ParseModels extracts all models from the OpenAPI spec
func (p *Parser) ParseModels() []Model {
	var models []Model

	// Iterate through schemas in the order they appear
	// After preprocessing, all inline schemas are now top-level schemas
	for name, schemaRef := range p.doc.Components.Schemas.Iter() {
		schema := schemaRef.Value
		if schemaIsEmpty(schema) {
			continue
		}
		pkg, _ := schema.Extensions["x-package"].(string)
		models = append(models, Model{
			SchemaName: name,
			Schema:     schema,
			Package:    pkg,
			Namespace:  schemaNamespace(name),
		})
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].SchemaName < models[j].SchemaName
	})

	return models
}

// schemaIsEmpty determines if a schema should not generate a file
func schemaIsEmpty(schema *openapi3.Schema) bool {
	if schema == nil {
		return true
	}

	// Skip schemas with no Type, no Properties, no Enum, and no composition (allOf/oneOf/anyOf)
	// These are essentially empty and should map to interface{} when referenced
	if schema.Type == nil &&
		schema.Properties.Len() == 0 &&
		len(schema.Enum) == 0 &&
		len(schema.AllOf) == 0 &&
		len(schema.OneOf) == 0 &&
		len(schema.AnyOf) == 0 {
		return true
	}

	return false
}

// PathOperation pairs an openapi3.Operation with its path and method
type PathOperation struct {
	Path      string
	Method    string
	Operation *openapi3.Operation
	// PathItem is the parent path item, retained so parameter helpers can merge
	// path-level parameters that every operation on the path inherits.
	PathItem *openapi3.PathItem
	vars     map[string]string
}

func (o PathOperation) Var(key, fallback string) string {
	if v, ok := o.vars[key]; ok {
		return v
	}
	return fallback
}

// SetVars sets the template variables exposed to this operation via Var.
func (o *PathOperation) SetVars(vars map[string]string) {
	o.vars = vars
}

// ParseOperations extracts all operations from the OpenAPI spec
// Sorted by tag (alphabetically) then by operation ID (alphabetically)
func (p *Parser) ParseOperations() []PathOperation {
	var operations []PathOperation

	for path, pathItem := range p.doc.Paths.Iter() {
		ops := []struct {
			method string
			op     *openapi3.Operation
		}{
			{"GET", pathItem.Get},
			{"POST", pathItem.Post},
			{"PUT", pathItem.Put},
			{"DELETE", pathItem.Delete},
			{"PATCH", pathItem.Patch},
		}

		for _, o := range ops {
			if o.op == nil || o.op.OperationID == "" {
				continue
			}
			operations = append(operations, PathOperation{
				Path:      path,
				Method:    o.method,
				Operation: o.op,
				PathItem:  pathItem,
			})
		}
	}

	// Sort by tag first, then by operation ID
	sort.Slice(operations, func(i, j int) bool {
		tagI := ""
		tagJ := ""
		if len(operations[i].Operation.Tags) > 0 {
			tagI = operations[i].Operation.Tags[0]
		}
		if len(operations[j].Operation.Tags) > 0 {
			tagJ = operations[j].Operation.Tags[0]
		}
		if tagI != tagJ {
			return tagI < tagJ
		}
		return operations[i].Operation.OperationID < operations[j].Operation.OperationID
	})

	return operations
}
