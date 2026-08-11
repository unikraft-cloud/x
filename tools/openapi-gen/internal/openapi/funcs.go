// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package openapi

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
	models *[]Model
	vars   *map[string]string
}

// Funcs returns the template function map bound to the given parser, models
// and variables. It is the entry point used by callers outside this package.
func Funcs(parser *Parser, models *[]Model, vars *map[string]string) template.FuncMap {
	return templateFuncs{parser: parser, models: models, vars: vars}.Funcs()
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

	// Other string helpers
	funcs["capitalize"] = capitalize
	funcs["wrapComment"] = tf.wrapComment

	// Type helpers
	funcs["refName"] = extractTypeFromRef
	funcs["getType"] = tf.getType
	funcs["getProperty"] = tf.getProperty
	funcs["getPropertyRequired"] = tf.getPropertyRequired

	// Properties, flattening allOf/oneOf/anyOf composition, in spec order.
	funcs["properties"] = tf.properties

	// Custom extensions
	funcs["getTypePackage"] = tf.getTypePackage

	// Add go helpers
	funcs["schemaToGoType"] = tf.schemaToGoType
	funcs["paramToGoType"] = tf.paramToGoType
	funcs["enumBaseGoType"] = tf.enumGoBaseType
	funcs["goSafeName"] = goSafeName

	// Add TypeScript helpers
	funcs["schemaToTsType"] = tf.schemaToTsType
	funcs["paramToTsType"] = tf.paramToTsType
	funcs["enumTsBaseType"] = tf.enumTsBaseType
	funcs["enumTsValue"] = tf.enumTsValue
	funcs["tsSafeName"] = tsSafeName
	funcs["tsDoc"] = tsDoc
	funcs["tsTypeRef"] = tf.tsTypeRef
	funcs["qualifyModels"] = tf.qualifyModels

	// Operation introspection helpers (language-neutral)
	funcs["pathParameters"] = tf.pathParameters
	funcs["queryParameters"] = tf.queryParameters
	funcs["requestJSONSchema"] = tf.requestJSONSchema
	funcs["requestBodyRequired"] = requestBodyRequired
	funcs["responseJSONSchema"] = tf.responseJSONSchema

	// Misc
	funcs["enumValue"] = tf.enumValue
	funcs["inlineEnums"] = tf.inlineEnums
	funcs["uniqueTags"] = uniqueTags

	return funcs
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

func (tf *templateFuncs) inlineEnums(schema *openapi3.Schema) []inlineEnum {
	if schema == nil {
		return nil
	}
	var result []inlineEnum
	for _, prop := range tf.properties(schema) {
		if prop.Schema == nil {
			continue
		}
		if len(prop.Schema.Enum) > 0 && prop.Schema.Title != "" {
			result = append(result, inlineEnum{Name: prop.Schema.Title, Schema: prop.Schema})
		}
		if prop.Schema.Type != nil && prop.Schema.Type.Is("array") && prop.Schema.Items != nil && prop.Schema.Items.Ref == "" && prop.Schema.Items.Value != nil {
			item := prop.Schema.Items.Value
			if len(item.Enum) > 0 && item.Title != "" {
				result = append(result, inlineEnum{Name: item.Title, Schema: item})
			}
		}
	}
	return result
}

// getType returns the OpenAPI type safely
func (tf *templateFuncs) getType(schema *openapi3.Schema) string {
	if schema == nil || schema.Type == nil || len(schema.Type.Slice()) == 0 {
		return ""
	}
	return schema.Type.Slice()[0]
}

// Property pairs a property name with its resolved schema. Returned by
// properties for templates that range over every property of a schema and
// need both, without a separate getProperty lookup per name.
type Property struct {
	Name   string
	Schema *openapi3.Schema
}

// properties returns every property contributed by schema, flattening
// allOf/oneOf/anyOf composition, in spec order.
func (tf *templateFuncs) properties(schema *openapi3.Schema) []Property {
	if schema == nil {
		return nil
	}
	props := schemaProperties(schema, true)
	result := make([]Property, len(props))
	for i, prop := range props {
		result[i] = Property{Name: prop.name, Schema: normalizedPropSchema(prop.ref)}
	}
	return result
}

// getProperty returns the schema of a named property, flattening allOf/oneOf/
// anyOf composition to find it.
func (tf *templateFuncs) getProperty(schema *openapi3.Schema, name string) *openapi3.Schema {
	if schema == nil {
		return nil
	}

	for _, prop := range schemaProperties(schema, true) {
		if prop.name == name {
			return normalizedPropSchema(prop.ref)
		}
	}

	return nil
}

// normalizedPropSchema returns ref's schema, normalising a direct $ref into
// the allOf-wrapped form so callers treat it like other optional fields:
// schemaToGoType extracts the type and getType returns "", so optional $ref
// fields get a pointer prefix.
func normalizedPropSchema(ref *openapi3.SchemaRef) *openapi3.Schema {
	if ref.Ref != "" {
		return &openapi3.Schema{
			AllOf: []*openapi3.SchemaRef{{Ref: ref.Ref}},
		}
	}
	return ref.Value
}

// getPropertyRequired checks if a property is required (checking allOf too).
// Properties that exist only inside oneOf branches are never considered required,
// since only one branch is active at runtime and they always need pointer types.
func (tf *templateFuncs) getPropertyRequired(schema *openapi3.Schema, propName string) bool {
	if schema == nil {
		return false
	}

	// If the property is defined inside a oneOf branch, it's never required
	// at the top level - only one branch is active at runtime.
	if tf.isInOneOf(schema, propName) {
		return false
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
			if _, ok := s.Properties.Get(propName); ok {
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
				if _, ok := s.Properties.Get(propName); ok {
					return true
				}
			}
		}
	}

	return false
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

// getTypePackage returns the x-package value for a type reference.
// Accepts *openapi3.Schema, *openapi3.SchemaRef, *openapi3.Parameter, or string ($ref).
// Returns empty string if no x-package is found.
//
// Deprecated: x-package is only emitted by our proto-based generation
// pipeline. New templates that need to distinguish local from foreign type
// references should compare against the "current_package" var (set from
// --namespace) instead.
func (tf *templateFuncs) getTypePackage(v any) string {
	ref := tf.extractRef(v)
	if ref == "" {
		return ""
	}

	typeName := extractTypeFromRef(ref)
	if typeName == "" {
		return ""
	}

	if schemaRef, ok := tf.parser.doc.Components.Schemas.Get(typeName); ok {
		if schemaRef.Value != nil {
			if pkg, _ := schemaRef.Value.Extensions["x-package"].(string); pkg != "" {
				return pkg
			}
		}
	}

	return ""
}

// extractRef extracts a $ref string from various OpenAPI types.
func (tf *templateFuncs) extractRef(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case *openapi3.SchemaRef:
		if val == nil {
			return ""
		}
		if val.Ref != "" {
			return val.Ref
		}
		if val.Value != nil {
			return tf.extractRef(val.Value)
		}
		return ""
	case *openapi3.Schema:
		if val == nil {
			return ""
		}
		// allOf with a single $ref (common pattern: allOf: [- $ref: ...])
		// Multi-element allOf (e.g., extend + extra props) is intentionally
		// not matched — those represent composite types, not references.
		if len(val.AllOf) == 1 && val.AllOf[0].Ref != "" {
			return val.AllOf[0].Ref
		}
		// Array items ref
		if val.Items != nil && val.Items.Ref != "" {
			return val.Items.Ref
		}
		return ""
	case *openapi3.Parameter:
		if val == nil || val.Schema == nil {
			return ""
		}
		return tf.extractRef(val.Schema)
	default:
		return ""
	}
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
		if s, ok := parser.doc.Components.Schemas.Get(name); ok {
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
