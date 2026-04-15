// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"
)

// Parser handles parsing OpenAPI specs into our template data structures
type Parser struct {
	doc            *openapi3.T
	propertyOrders map[string][]string // schemaName -> ordered property names
}

// Model represents a single model file to be generated
type Model struct {
	SchemaName string
	Schema     *openapi3.Schema
}

// isURL reports whether s looks like an HTTP(S) URL.
func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// readSpec reads the OpenAPI specification bytes from a local file path or
// an HTTP(S) URL.
func readSpec(input string) ([]byte, error) {
	if isURL(input) {
		resp, err := http.Get(input) //nolint:gosec,noctx
		if err != nil {
			return nil, fmt.Errorf("fetching spec from URL: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetching spec from URL: HTTP %d", resp.StatusCode)
		}
		return io.ReadAll(resp.Body)
	}
	if g := parseGitRef(input); g != nil {
		return readSpecFromGit(g)
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
	case parseGitRef(input) != nil:
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

	// HACK: we need to ensure operations are processed in a consistent order
	// we need to do some YAML hackery to extract property order
	// see https://github.com/getkin/kin-openapi/pull/695
	propertyOrders, err := extractPropertyOrders(data)
	if err != nil {
		return nil, fmt.Errorf("extracting property orders: %w", err)
	}

	return &Parser{
		doc:            doc,
		propertyOrders: propertyOrders,
	}, nil
}

// GetPropertyOrder returns the ordered property names for a schema
func (p *Parser) GetPropertyOrder(schemaName string) []string {
	return p.propertyOrders[schemaName]
}

// SetPropertyOrder registers the property order for a schema
// This is used by the preprocessor to register inline schemas
func (p *Parser) SetPropertyOrder(schemaName string, order []string) {
	p.propertyOrders[schemaName] = order
}

// ParseModels extracts all models from the OpenAPI spec
func (p *Parser) ParseModels() []Model {
	var models []Model

	// Iterate through schemas in the order they appear
	// After preprocessing, all inline schemas are now top-level schemas
	for name, schemaRef := range p.doc.Components.Schemas {
		schema := schemaRef.Value
		if schemaIsEmpty(schema) {
			continue
		}
		models = append(models, Model{
			SchemaName: name,
			Schema:     schema,
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
		len(schema.Properties) == 0 &&
		len(schema.Enum) == 0 &&
		len(schema.AllOf) == 0 &&
		len(schema.OneOf) == 0 &&
		len(schema.AnyOf) == 0 {
		return true
	}

	return false
}

// extractPropertyOrders parses the YAML to get property order for all schemas
func extractPropertyOrders(data []byte) (map[string][]string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}

	orders := make(map[string][]string)
	extractFromNode(&root, orders)
	return orders, nil
}

// extractFromNode recursively extracts property orders from YAML nodes
func extractFromNode(node *yaml.Node, orders map[string][]string) {
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		extractFromNode(node.Content[0], orders)
		return
	}

	if node.Kind == yaml.MappingNode {
		// Check if this is a schema definition in components/schemas
		var currentSchemaName string

		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			value := node.Content[i+1]

			// Detect if we're in a schema by looking for type/properties/allOf
			if key.Value == "type" || key.Value == "properties" || key.Value == "allOf" {
				// This is likely a schema, extract its properties
				propOrder := extractPropertiesFromSchema(node)
				if len(propOrder) > 0 && currentSchemaName != "" {
					orders[currentSchemaName] = propOrder
				}
			}

			// Track schema names when we see them
			if value.Kind == yaml.MappingNode {
				// Check if this looks like a schema definition
				if hasSchemaKeys(value) {
					currentSchemaName = key.Value
					propOrder := extractPropertiesFromSchema(value)
					if len(propOrder) > 0 {
						orders[key.Value] = propOrder
					}
				}
				extractFromNode(value, orders)
			}
		}
	}
}

// hasSchemaKeys checks if a node has keys that indicate it's a schema
func hasSchemaKeys(node *yaml.Node) bool {
	if node.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if key == "type" || key == "properties" || key == "allOf" || key == "oneOf" || key == "anyOf" {
			return true
		}
	}
	return false
}

// extractPropertiesFromSchema extracts ordered property names from a schema node
func extractPropertiesFromSchema(node *yaml.Node) []string {
	var props []string

	if node.Kind != yaml.MappingNode {
		return props
	}

	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		value := node.Content[i+1]

		// Direct properties
		if key.Value == "properties" && value.Kind == yaml.MappingNode {
			for j := 0; j < len(value.Content); j += 2 {
				propName := value.Content[j].Value
				if !slices.Contains(props, propName) {
					props = append(props, propName)
				}
			}
		}

		// Properties in allOf
		if key.Value == "allOf" && value.Kind == yaml.SequenceNode {
			for _, item := range value.Content {
				allOfProps := extractPropertiesFromSchema(item)
				for _, prop := range allOfProps {
					if !slices.Contains(props, prop) {
						props = append(props, prop)
					}
				}
			}
		}

		// Properties in oneOf (within allOf)
		if key.Value == "oneOf" && value.Kind == yaml.SequenceNode {
			for _, item := range value.Content {
				oneOfProps := extractPropertiesFromSchema(item)
				for _, prop := range oneOfProps {
					if !slices.Contains(props, prop) {
						props = append(props, prop)
					}
				}
			}
		}
	}

	return props
}

// PathOperation pairs an openapi3.Operation with its path and method
type PathOperation struct {
	Path      string
	Method    string
	Operation *openapi3.Operation
	vars      map[string]string
}

func (o PathOperation) Var(key, fallback string) string {
	if v, ok := o.vars[key]; ok {
		return v
	}
	return fallback
}

// ParseOperations extracts all operations from the OpenAPI spec
// Sorted by tag (alphabetically) then by operation ID (alphabetically)
func (p *Parser) ParseOperations() []PathOperation {
	var operations []PathOperation

	// HACK: we need to ensure operations are processed in a consistent order
	// it just so turns out our input schemas are already sorted alphabetically
	// by path, so we can leverage that to get a consistent order of operations
	// see https://github.com/getkin/kin-openapi/pull/695

	for path, pathItem := range p.doc.Paths.Map() {
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
