// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"text/template"
	"unicode"
	"unicode/utf8"

	"github.com/Masterminds/sprig/v3"
	"github.com/ettle/strcase"
	"github.com/getkin/kin-openapi/openapi3"
	wordwrap "github.com/mitchellh/go-wordwrap"
)

// templateFuncs holds the parser and provides template helper functions
type templateFuncs struct {
	parser *Parser
}

// Funcs returns all custom template functions
func (tf templateFuncs) Funcs() template.FuncMap {
	funcs := sprig.TxtFuncMap()

	// Add strcase functions
	funcs["pascalcase"] = strcase.ToPascal // overrides sprig
	funcs["camelcase"] = strcase.ToCamel   // overrides sprig
	funcs["snakecase"] = strcase.ToSnake   // overrides sprig
	funcs["kebabcase"] = strcase.ToKebab   // overrides sprig
	funcs["screamingsnakecase"] = strcase.ToSNAKE

	// Add custom functions
	funcs["capitalize"] = capitalize
	funcs["capitalizeEnum"] = capitalizeEnum

	// Add schema helper functions
	funcs["schemaToGoType"] = tf.schemaToGoType
	funcs["collectImports"] = tf.collectImports

	// Helper to check if schema is an enum
	funcs["isEnum"] = tf.isEnum

	// Helper to check if schema is a specific type
	funcs["isType"] = tf.isType

	// Helper to get OpenAPI type safely
	funcs["getType"] = tf.getType

	// Helper to get property names in the order they appear in YAML
	funcs["propertyNamesOrdered"] = tf.propertyNamesOrdered

	// Helper to get a property schema (checking allOf too)
	funcs["getProperty"] = tf.getProperty

	// Helper to check if a property is required (checking allOf too)
	funcs["isRequired"] = tf.isRequired

	// Helper to check if a specific property is defined within a oneOf
	funcs["isInOneOf"] = tf.isInOneOf

	// Helper to extract type from $ref
	funcs["refToType"] = tf.refToType

	// Helper to generate docs URL from operation
	funcs["docsURL"] = tf.docsURL

	// Helper to convert OpenAPI parameter to Go type
	funcs["paramToGoType"] = tf.paramToGoType

	// Helper to wrap comments at a certain width
	funcs["wrapComment"] = tf.wrapComment

	funcs["safeName"] = safeName
	funcs["enumBaseType"] = tf.enumBaseType
	funcs["enumValue"] = tf.enumValue
	funcs["inlineEnums"] = tf.inlineEnums
	funcs["sortedResponseCodes"] = sortedResponseCodes
	funcs["sortedContentTypes"] = sortedContentTypes
	funcs["uniqueTags"] = uniqueTags

	return funcs
}

// schemaToGoType converts an OpenAPI schema to a Go type string
func (tf *templateFuncs) schemaToGoType(schema *openapi3.Schema) string {
	return schemaToGoTypeWithParser(schema, tf.parser, false)
}

// paramToGoType converts OpenAPI parameter to Go type
func (tf *templateFuncs) paramToGoType(param *openapi3.Parameter) string {
	return schemaToGoTypeWithParser(param.Schema.Value, tf.parser, true)
}

// collectImports determines what imports are needed for a schema
func (tf *templateFuncs) collectImports(schema *openapi3.Schema) []string {
	if schema == nil {
		return nil
	}

	imports := make(map[string]bool)
	visited := make(map[*openapi3.Schema]bool)

	var checkSchema func(*openapi3.Schema)
	checkSchema = func(s *openapi3.Schema) {
		if s == nil {
			return
		}

		// Avoid infinite recursion
		if visited[s] {
			return
		}
		visited[s] = true

		// Check for time.Time - only in properties that are inline (not refs)
		if s.Type != nil && s.Type.Is("string") && s.Format == "date-time" {
			imports["time"] = true
		}

		// Check properties - only inline values, not refs
		// Refs are separate types with their own imports
		for _, propRef := range s.Properties {
			// Skip properties that are refs - they have their own files/imports
			if propRef.Ref != "" {
				continue
			}
			if propRef.Value != nil {
				checkSchema(propRef.Value)
			}
		}

		// Check array items - only if inline (not refs)
		if s.Items != nil && s.Items.Ref == "" && s.Items.Value != nil {
			checkSchema(s.Items.Value)
		}

		// DON'T check composed schemas (allOf/oneOf/anyOf) if they are refs
		// Only check inline composed schemas
		for _, ref := range s.AllOf {
			if ref.Ref == "" && ref.Value != nil {
				checkSchema(ref.Value)
			}
		}
		for _, ref := range s.OneOf {
			if ref.Ref == "" && ref.Value != nil {
				checkSchema(ref.Value)
			}
		}
		for _, ref := range s.AnyOf {
			if ref.Ref == "" && ref.Value != nil {
				checkSchema(ref.Value)
			}
		}
	}

	checkSchema(schema)

	var result []string
	for imp := range imports {
		result = append(result, imp)
	}
	return result
}

// isEnum checks if schema is an enum
func (tf *templateFuncs) isEnum(schema *openapi3.Schema) bool {
	return schema != nil && len(schema.Enum) > 0
}

func (tf *templateFuncs) enumBaseType(schema *openapi3.Schema) string {
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

func (tf *templateFuncs) enumValue(schema *openapi3.Schema, val any) string {
	if schema != nil && !schema.Type.Is("string") {
		return fmt.Sprintf("%v", val)
	}
	return fmt.Sprintf("%q", fmt.Sprintf("%v", val))
}

type inlineEnum struct {
	Name   string
	Schema *openapi3.Schema
}

func (tf *templateFuncs) inlineEnums(schemaName string, schema *openapi3.Schema) []inlineEnum {
	if schema == nil {
		return nil
	}
	var result []inlineEnum
	for _, propName := range tf.propertyNamesOrdered(schemaName, schema) {
		prop := tf.getProperty(schema, propName)
		if prop == nil {
			continue
		}
		if len(prop.Enum) > 0 && prop.Title != "" {
			result = append(result, inlineEnum{Name: prop.Title, Schema: prop})
		}
		if prop.Type != nil && prop.Type.Is("array") && prop.Items != nil && prop.Items.Value != nil {
			item := prop.Items.Value
			if len(item.Enum) > 0 && item.Title != "" {
				result = append(result, inlineEnum{Name: item.Title, Schema: item})
			}
		}
	}
	return result
}

// isType checks if schema is a specific type
func (tf *templateFuncs) isType(schema *openapi3.Schema, typeName string) bool {
	return schema != nil && schema.Type.Is(typeName)
}

// getType returns the OpenAPI type safely
func (tf *templateFuncs) getType(schema *openapi3.Schema) string {
	if schema == nil || schema.Type == nil || len(schema.Type.Slice()) == 0 {
		return ""
	}
	return schema.Type.Slice()[0]
}

// propertyNamesOrdered returns property names in the order they appear in YAML
func (tf *templateFuncs) propertyNamesOrdered(schemaName string, schema *openapi3.Schema) []string {
	// First try to get order from YAML parser
	if order := tf.parser.GetPropertyOrder(schemaName); len(order) > 0 {
		return order
	}

	// Fallback to collecting from schema (unordered)
	if schema == nil {
		return nil
	}

	names := make([]string, 0)
	seen := make(map[string]bool)

	// Collect from allOf
	if len(schema.AllOf) > 0 {
		for _, allOfRef := range schema.AllOf {
			allOfSchema := allOfRef.Value
			if allOfSchema != nil && len(allOfSchema.Properties) > 0 {
				for name := range allOfSchema.Properties {
					if !seen[name] {
						names = append(names, name)
						seen[name] = true
					}
				}
			}
			if allOfSchema != nil && len(allOfSchema.OneOf) > 0 {
				for _, oneOfRef := range allOfSchema.OneOf {
					oneOfSchema := oneOfRef.Value
					if oneOfSchema != nil && len(oneOfSchema.Properties) > 0 {
						for name := range oneOfSchema.Properties {
							if !seen[name] {
								names = append(names, name)
								seen[name] = true
							}
						}
					}
				}
			}
		}
	}

	// Collect from direct properties
	if len(schema.Properties) > 0 {
		for name := range schema.Properties {
			if !seen[name] {
				names = append(names, name)
				seen[name] = true
			}
		}
	}

	return names
}

// getProperty returns a property schema (checking allOf too)
func (tf *templateFuncs) getProperty(schema *openapi3.Schema, name string) *openapi3.Schema {
	if schema == nil {
		return nil
	}

	var propRef *openapi3.SchemaRef

	// Check direct properties first
	if len(schema.Properties) > 0 {
		if ref, ok := schema.Properties[name]; ok {
			propRef = ref
		}
	}

	// Check in allOf schemas if not found
	if propRef == nil && len(schema.AllOf) > 0 {
		for _, allOfRef := range schema.AllOf {
			allOfSchema := allOfRef.Value
			if allOfSchema != nil && len(allOfSchema.Properties) > 0 {
				if ref, ok := allOfSchema.Properties[name]; ok {
					propRef = ref
					break
				}
			}
			// Also check in oneOf within allOf
			if allOfSchema != nil && len(allOfSchema.OneOf) > 0 {
				for _, oneOfRef := range allOfSchema.OneOf {
					oneOfSchema := oneOfRef.Value
					if oneOfSchema != nil && len(oneOfSchema.Properties) > 0 {
						if ref, ok := oneOfSchema.Properties[name]; ok {
							propRef = ref
							break
						}
					}
				}
				if propRef != nil {
					break
				}
			}
		}
	}

	if propRef == nil {
		return nil
	}

	// If it's a $ref, create a simple schema with allOf pointing to the ref.
	// This ensures consistent handling between direct $ref and allOf: [$ref],
	// allowing schemaToGoType to extract the type and getType to return ""
	// (so optional $ref fields get pointer prefixes like other optional fields).
	if propRef.Ref != "" {
		return &openapi3.Schema{
			AllOf: []*openapi3.SchemaRef{
				{Ref: propRef.Ref},
			},
		}
	}

	return propRef.Value
}

// isRequired checks if a property is required (checking allOf too)
func (tf *templateFuncs) isRequired(schema *openapi3.Schema, propName string) bool {
	if schema == nil {
		return false
	}

	// Check if this is an inline schema (created by preprocessor)
	// Inline schemas treat oneOf branch-required fields as required
	isInlineSchema := false
	if schema.Extensions != nil {
		if _, ok := schema.Extensions["x-inline-schema"]; ok {
			isInlineSchema = true
		}
	}

	// Check direct required
	if slices.Contains(schema.Required, propName) {
		return true
	}

	// Check in allOf schemas
	if len(schema.AllOf) > 0 {
		for _, allOfRef := range schema.AllOf {
			allOfSchema := allOfRef.Value
			if allOfSchema != nil {
				if slices.Contains(allOfSchema.Required, propName) {
					return true
				}

				// For oneOf within allOf (discriminated unions), if this is an inline schema
				// and a property is required in ANY branch, treat it as required overall.
				// For standalone schemas, keep fields optional (pointer types).
				if isInlineSchema && len(allOfSchema.OneOf) > 0 {
					for _, oneOfRef := range allOfSchema.OneOf {
						oneOfSchema := oneOfRef.Value
						if oneOfSchema != nil {
							// Check if this oneOf branch has the property
							_, hasProperty := oneOfSchema.Properties[propName]
							if hasProperty {
								// Check if it's required in this branch
								if slices.Contains(oneOfSchema.Required, propName) {
									return true
								}
							}
						}
					}
				}
			}
		}
	}

	return false
}

// isInOneOf reports whether propName is defined inside a oneOf branch
func (tf *templateFuncs) isInOneOf(schema *openapi3.Schema, propName string) bool {
	if schema == nil {
		return false
	}

	for _, ref := range schema.OneOf {
		if s := resolveTypeFromRef(tf.parser, ref); s != nil {
			if _, ok := s.Properties[propName]; ok {
				return true
			}
		}
	}

	for _, allOfRef := range schema.AllOf {
		allOfSchema := resolveTypeFromRef(tf.parser, allOfRef)
		if allOfSchema == nil {
			continue
		}

		for _, ref := range allOfSchema.OneOf {
			if s := resolveTypeFromRef(tf.parser, ref); s != nil {
				if _, ok := s.Properties[propName]; ok {
					return true
				}
			}
		}
	}

	return false
}

// refToType extracts type from $ref
func (tf *templateFuncs) refToType(ref string) string {
	return extractTypeFromRef(ref)
}

// docsURL generates docs URL from operation
func (tf *templateFuncs) docsURL(op *openapi3.Operation) string {
	if len(op.Tags) == 0 {
		return ""
	}
	// Convert tag from PascalCase to kebab-case (e.g., ServiceGroups -> service-groups)
	tag := strcase.ToKebab(op.Tags[0])
	// Convert OperationID from PascalCase to kebab-case
	opIDKebab := strcase.ToKebab(op.OperationID)
	// FIXME: handle for controlplane
	return "https://unikraft.com/docs/api/platform/v1/" + tag + "#" + opIDKebab
}

// wrapComment wraps text at a certain width, adding prefix to continuation lines
func (tf *templateFuncs) wrapComment(text string, width int, prefix string) string {
	if text == "" {
		return ""
	}
	wrapped := wordwrap.WrapString(text, uint(width))
	lines := strings.Split(wrapped, "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
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

// extractTypeFromRef extracts the type name from a $ref string
// e.g., "#/components/schemas/AutoscalePolicyStep" -> "AutoscalePolicyStep"
func extractTypeFromRef(ref string) string {
	parts := strings.Split(ref, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

// resolveTypeFromRef returns the Schema for a SchemaRef, preferring the
// already-resolved Value and falling back to a components/schemas lookup
// for $ref entries where Value has not been populated by the loader.
func resolveTypeFromRef(parser *Parser, ref *openapi3.SchemaRef) *openapi3.Schema {
	if ref == nil {
		return nil
	}

	if ref.Value != nil {
		return ref.Value
	}

	if ref.Ref != "" {
		name := extractTypeFromRef(ref.Ref)
		if s, ok := parser.doc.Components.Schemas[name]; ok {
			return s.Value
		}
	}
	return nil
}

// capitalize converts first letter to uppercase (unicode-safe)
func capitalize(s string) string {
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

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

func safeName(s string) string {
	if _, found := slices.BinarySearch(goReservedWords, s); found {
		return "_" + s
	}
	return s
}

// ResponseEntry is a code/ref pair for deterministic template iteration.
type ResponseEntry struct {
	Code string
	Ref  *openapi3.ResponseRef
}

// sortedResponseCodes returns response entries sorted by status code.
func sortedResponseCodes(responses *openapi3.Responses) []ResponseEntry {
	if responses == nil {
		return nil
	}
	m := responses.Map()
	entries := make([]ResponseEntry, 0, len(m))
	for code, ref := range m {
		entries = append(entries, ResponseEntry{Code: code, Ref: ref})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Code < entries[j].Code
	})
	return entries
}

// ContentEntry is a content-type/media pair for deterministic template iteration.
type ContentEntry struct {
	Type  string
	Media *openapi3.MediaType
}

// sortedContentTypes returns content entries sorted by media type.
func sortedContentTypes(content openapi3.Content) []ContentEntry {
	if content == nil {
		return nil
	}
	entries := make([]ContentEntry, 0, len(content))
	for ct, media := range content {
		entries = append(entries, ContentEntry{Type: ct, Media: media})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Type < entries[j].Type
	})
	return entries
}

// uniqueTags returns deduplicated, sorted tags from operations.
func uniqueTags(operations []PathOperation) []string {
	seen := make(map[string]bool)
	var tags []string
	for _, op := range operations {
		if op.Operation != nil && len(op.Operation.Tags) > 0 {
			for _, tag := range op.Operation.Tags {
				if !seen[tag] {
					seen[tag] = true
					tags = append(tags, tag)
				}
			}
		}
	}
	sort.Strings(tags)
	return tags
}

// capitalizeEnum handles enum constant naming for top-level enum schemas
// (all-caps for single lowercase words, title-case otherwise)
func capitalizeEnum(s string) string {
	if s == "" {
		return s
	}
	// If the string is all lowercase letters (no underscores, spaces, etc.), uppercase it all
	allLower := true
	for _, r := range s {
		if r < 'a' || r > 'z' {
			allLower = false
			break
		}
	}
	if allLower {
		return strings.ToUpper(s)
	}
	// Otherwise just capitalize first letter
	return capitalize(s)
}
