// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"slices"

	"github.com/getkin/kin-openapi/openapi3"
)

var goReservedWords = []string{
	"break",
	"case",
	"chan",
	"const",
	"continue",
	"default",
	"defer",
	"else",
	"fallthrough",
	"for",
	"func",
	"go",
	"goto",
	"if",
	"import",
	"interface",
	"map",
	"package",
	"range",
	"return",
	"select",
	"struct",
	"switch",
	"type",
	"var",
}

func goSafeName(s string) string {
	if _, found := slices.BinarySearch(goReservedWords, s); found {
		return "_" + s
	}
	return s
}

// schemaToGoType converts an OpenAPI schema to a Go type string
func (tf *templateFuncs) schemaToGoType(schema *openapi3.Schema) string {
	return schemaToGoTypeWithParser(schema, tf.parser, false)
}

// paramToGoType converts OpenAPI parameter to Go type
func (tf *templateFuncs) paramToGoType(param *openapi3.Parameter) string {
	// If the parameter schema is a $ref, return the referenced type name
	if param.Schema != nil && param.Schema.Ref != "" {
		return extractTypeFromRef(param.Schema.Ref)
	}
	return schemaToGoTypeWithParser(param.Schema.Value, tf.parser, false)
}

// schemaToGoTypeWithParser converts an OpenAPI schema type to a Go type
// This version has access to the parser to check if referenced schemas should be skipped
func schemaToGoTypeWithParser(schema *openapi3.Schema, parser *Parser, useLegacyInt bool) string {
	if schema == nil {
		return "interface{}"
	}

	// Handle allOf with a single $ref (common pattern for type aliasing)
	if len(schema.AllOf) == 1 && schema.AllOf[0].Ref != "" {
		refType := extractTypeFromRef(schema.AllOf[0].Ref)

		// Check if the referenced schema should be skipped (e.g., GoogleProtobufValue)
		// If so, return interface{} instead
		if refSchemaRef, ok := parser.doc.Components.Schemas[refType]; ok {
			if schemaIsEmpty(refSchemaRef.Value) {
				return "interface{}"
			}
		}

		return refType
	}

	// Delegate to the original function for the rest
	return schemaToGoType(schema, useLegacyInt)
}

// schemaToGoType converts an OpenAPI schema type to a Go type
func schemaToGoType(schema *openapi3.Schema, useLegacyInt bool) string {
	if schema == nil {
		return "interface{}"
	}

	// Handle allOf with a single $ref (common pattern for type aliasing)
	// Note: schemaToGoTypeWithParser handles this case with skip-schema checking,
	// but we keep it here for direct callers of schemaToGoType
	if len(schema.AllOf) == 1 && schema.AllOf[0].Ref != "" {
		return extractTypeFromRef(schema.AllOf[0].Ref)
	}

	// Handle arrays
	if schema.Type.Is("array") {
		if schema.Items != nil && schema.Items.Ref != "" {
			// Extract type name from $ref
			refType := extractTypeFromRef(schema.Items.Ref)
			return "[]" + refType
		}
		if schema.Items != nil {
			itemType := schemaToGoType(schema.Items.Value, useLegacyInt)
			return "[]" + itemType
		}
		return "[]interface{}"
	}

	// Handle inline enums
	if len(schema.Enum) > 0 {
		if schema.Title != "" {
			return schema.Title
		}
		return "string"
	}

	// Handle object/map types
	if schema.Type.Is("object") {
		// Check if it has additionalProperties defined
		if schema.AdditionalProperties.Schema != nil {
			valueType := schemaToGoType(schema.AdditionalProperties.Schema.Value, useLegacyInt)
			return "map[string]" + valueType
		}
		if schema.AdditionalProperties.Has != nil && *schema.AdditionalProperties.Has {
			return "map[string]interface{}"
		}
		// If it has properties, it's a struct type
		if len(schema.Properties) > 0 {
			if schema.Title != "" {
				return schema.Title
			}
		}
		return "map[string]interface{}"
	}

	// Basic types
	switch {
	case schema.Type.Is("string"):
		if schema.Format == "date-time" {
			return "time.Time"
		}
		return "string"
	case schema.Type.Is("integer"):
		if useLegacyInt {
			if schema.Format == "int32" || schema.Format == "uint32" ||
				schema.Format == "int64" || schema.Format == "uint64" {
				return "int32"
			}
		}
		if schema.Format != "" {
			return schema.Format
		}
		return "int"
	case schema.Type.Is("number"):
		if schema.Format == "float" {
			return "float32"
		}
		return "float64"
	case schema.Type.Is("boolean"):
		return "bool"
	}

	return "interface{}"
}

func (tf *templateFuncs) enumGoBaseType(schema *openapi3.Schema) string {
	if schema == nil {
		return "string"
	}

	switch {
	case schema.Type.Is("integer"):
		if schema.Format != "" {
			return schema.Format
		}
		return "int"
	case schema.Type.Is("number"):
		if schema.Format == "float" {
			return "float32"
		}
		return "float64"
	case schema.Type.Is("boolean"):
		return "bool"
	default:
		return "string"
	}
}
