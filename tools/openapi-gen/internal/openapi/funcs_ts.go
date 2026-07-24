// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package openapi

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	wordwrap "github.com/mitchellh/go-wordwrap"
)

// sprintfTs stringifies a value using Go's default formatting; used for numeric
// and boolean enum members.
func sprintfTs(v any) string {
	return fmt.Sprintf("%v", v)
}

// tsReservedWords are identifiers that cannot be used unescaped as TypeScript
// identifiers (class names, method names, enum members). Property keys inside
// interfaces are always emitted quoted by the templates, so they do not need
// escaping; this list guards generated symbol names only. `constructor` is
// included because a method named `constructor` is emitted as the class
// constructor (and an `async constructor` is invalid). Kept sorted for binary
// search.
var tsReservedWords = []string{
	"any",
	"as",
	"async",
	"await",
	"boolean",
	"break",
	"case",
	"catch",
	"class",
	"const",
	"constructor",
	"continue",
	"debugger",
	"declare",
	"default",
	"delete",
	"do",
	"else",
	"enum",
	"export",
	"extends",
	"false",
	"finally",
	"for",
	"from",
	"function",
	"if",
	"implements",
	"import",
	"in",
	"instanceof",
	"interface",
	"let",
	"module",
	"namespace",
	"never",
	"new",
	"null",
	"number",
	"of",
	"package",
	"private",
	"protected",
	"public",
	"return",
	"static",
	"string",
	"super",
	"switch",
	"symbol",
	"this",
	"throw",
	"true",
	"try",
	"type",
	"typeof",
	"undefined",
	"unknown",
	"var",
	"void",
	"while",
	"with",
	"yield",
}

// tsSafeName prefixes a TypeScript reserved word with "_" so it can be used as
// an identifier. Mirrors goSafeName.
func tsSafeName(s string) string {
	if _, found := slices.BinarySearch(tsReservedWords, s); found {
		return "_" + s
	}
	return s
}

// schemaToTsType converts an OpenAPI schema to a TypeScript type string.
func (tf *templateFuncs) schemaToTsType(schema *openapi3.Schema) string {
	return schemaToTsTypeWithParser(schema, tf.parser)
}

// paramToTsType converts an OpenAPI parameter to a TypeScript type string.
func (tf *templateFuncs) paramToTsType(param *openapi3.Parameter) string {
	if param == nil {
		return "unknown"
	}
	// A $ref parameter schema resolves to the referenced type name, collapsing
	// to `unknown` when that component is an empty (skipped) schema.
	if param.Schema != nil && param.Schema.Ref != "" {
		return refTsName(tf.parser, param.Schema.Ref)
	}
	// Parameters may legally omit `schema` (e.g. when `content` is used).
	if param.Schema == nil {
		return "unknown"
	}
	return schemaToTsTypeWithParser(param.Schema.Value, tf.parser)
}

// schemaToTsType converts an OpenAPI schema to a TypeScript type string. It is a
// thin wrapper around schemaToTsTypeWithParser without a parser, so references
// to empty (skipped) schemas are not collapsed to `unknown`.
func schemaToTsType(schema *openapi3.Schema) string {
	return schemaToTsTypeWithParser(schema, nil)
}

// schemaToTsTypeWithParser converts an OpenAPI schema to a TypeScript type.
// When parser is non-nil, references to empty schemas (which ParseModels omits)
// collapse to `unknown` so generated code never names a type that is never
// emitted. The parser-aware check is applied recursively to nested references
// (array items, additionalProperties, composition branches). `nullable: true`
// schemas are widened with `| null`.
func schemaToTsTypeWithParser(schema *openapi3.Schema, parser *Parser) string {
	if schema == nil {
		return "unknown"
	}
	t := baseTsType(schema, parser)
	if schema.Nullable {
		return tsNullable(t)
	}
	return t
}

// baseTsType maps a schema to its TypeScript type, ignoring nullability (handled
// by schemaToTsTypeWithParser).
func baseTsType(schema *openapi3.Schema, parser *Parser) string {
	// allOf with a single $ref (type aliasing).
	if len(schema.AllOf) == 1 && schema.AllOf[0].Ref != "" {
		return refTsName(parser, schema.AllOf[0].Ref)
	}

	// Arrays.
	if schema.Type.Is("array") {
		if schema.Items != nil && schema.Items.Ref != "" {
			return tsArrayOf(refTsName(parser, schema.Items.Ref))
		}
		if schema.Items != nil {
			return tsArrayOf(schemaToTsTypeWithParser(schema.Items.Value, parser))
		}
		return "unknown[]"
	}

	// Named inline enums surface as their generated union type; otherwise fall
	// back to the enum's base type.
	if len(schema.Enum) > 0 {
		if schema.Title != "" {
			return schema.Title
		}
		return enumTsUnion(schema)
	}

	// Composition renders as union/intersection even when `type: object` is
	// present, so object-composed schemas don't collapse to generic records.
	// (Single-$ref allOf is handled above.)
	if c := tsComposite(schema, parser); c != "" {
		return c
	}

	// Objects and maps.
	if schema.Type.Is("object") {
		if schema.AdditionalProperties.Schema != nil {
			if schema.AdditionalProperties.Schema.Ref != "" {
				return "Record<string, " + refTsName(parser, schema.AdditionalProperties.Schema.Ref) + ">"
			}
			return "Record<string, " + schemaToTsTypeWithParser(schema.AdditionalProperties.Schema.Value, parser) + ">"
		}
		if schema.AdditionalProperties.Has != nil && *schema.AdditionalProperties.Has {
			return "Record<string, unknown>"
		}
		if len(schema.Properties) > 0 && schema.Title != "" {
			return schema.Title
		}
		if schema.AdditionalProperties.Has != nil && !*schema.AdditionalProperties.Has {
			return "Record<string, never>"
		}
		return "Record<string, unknown>"
	}

	if schema.Type == nil {
		return "unknown"
	}

	switch {
	case schema.Type.Is("string"):
		// date/date-time carried as ISO-8601 strings over JSON.
		return "string"
	case schema.Type.Is("integer"), schema.Type.Is("number"):
		return "number"
	case schema.Type.Is("boolean"):
		return "boolean"
	}

	return "unknown"
}

// tsComposite renders oneOf/anyOf as a union and multi-entry allOf as an
// intersection, resolving each branch (inline or $ref). Union branches inside an
// intersection are parenthesised so `&` binds correctly. Returns "" when the
// schema carries no such composition.
func tsComposite(schema *openapi3.Schema, parser *Parser) string {
	branch := func(ref *openapi3.SchemaRef) string {
		if ref == nil {
			return ""
		}
		if ref.Ref != "" {
			return refTsName(parser, ref.Ref)
		}
		return schemaToTsTypeWithParser(ref.Value, parser)
	}
	join := func(refs openapi3.SchemaRefs, sep string, parenUnion bool) string {
		parts := make([]string, 0, len(refs))
		for _, ref := range refs {
			p := branch(ref)
			if p == "" {
				continue
			}
			if parenUnion && strings.Contains(p, "|") {
				p = "(" + p + ")"
			}
			parts = append(parts, p)
		}
		return strings.Join(parts, sep)
	}
	switch {
	case len(schema.OneOf) > 0:
		return join(schema.OneOf, " | ", false)
	case len(schema.AnyOf) > 0:
		return join(schema.AnyOf, " | ", false)
	case len(schema.AllOf) > 0:
		return join(schema.AllOf, " & ", true)
	}
	return ""
}

// refTsName returns the TypeScript type name for a $ref string. When parser is
// non-nil and the referenced component is an empty schema (omitted by
// ParseModels), it returns "unknown" so generated code never names a type that
// is never emitted.
func refTsName(parser *Parser, ref string) string {
	name := extractTypeFromRef(ref)
	if parser != nil && parser.doc != nil && parser.doc.Components != nil {
		if sr, ok := parser.doc.Components.Schemas[name]; ok {
			if schemaIsEmpty(sr.Value) {
				return "unknown"
			}
		}
	}
	return name
}

// tsNullable widens a type with `| null`, parenthesising an intersection so the
// union binds correctly (e.g. "(A & B) | null").
func tsNullable(t string) string {
	if t == "" || t == "unknown" {
		return t
	}
	if strings.Contains(t, "&") {
		return "(" + t + ") | null"
	}
	return t + " | null"
}

// qualifyModels prefixes bare occurrences of known model names in a rendered
// TypeScript type with "<ns>." (e.g. "Instance[]" -> "models.Instance[]"). It
// tokenises the string and rewrites only identifier tokens that are not part of
// a quoted string literal and not already member accesses (preceded by "."), so
// primitives, `Record`, `unknown`, string enums, and already-qualified names are
// left untouched. An empty ns is a no-op.
func (tf *templateFuncs) qualifyModels(ns, tsType string) string {
	if ns == "" || tsType == "" || tf.models == nil {
		return tsType
	}
	names := make(map[string]struct{}, len(*tf.models))
	for _, m := range *tf.models {
		names[m.SchemaName] = struct{}{}
	}
	if len(names) == 0 {
		return tsType
	}
	// Match either a double-quoted string literal (skipped wholesale) or an
	// identifier optionally preceded by "." (a member access, also skipped).
	return tsIdentRe.ReplaceAllStringFunc(tsType, func(tok string) string {
		if tok[0] == '"' || tok[0] == '.' {
			return tok
		}
		if _, ok := names[tok]; ok {
			return ns + "." + tok
		}
		return tok
	})
}

// tsIdentRe matches a double-quoted TypeScript string literal or an identifier
// optionally preceded by a "." (member access).
var tsIdentRe = regexp.MustCompile(`"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|\.?[A-Za-z_$][A-Za-z0-9_$]*`)

// tsArrayOf wraps an element type as an array, parenthesising union elements so
// precedence is preserved (e.g. "(A | B)[]").
func tsArrayOf(elem string) string {
	if strings.Contains(elem, "|") || strings.Contains(elem, "&") {
		return "(" + elem + ")[]"
	}
	return elem + "[]"
}

// enumTsBaseType returns the TypeScript primitive backing an enum.
func (tf *templateFuncs) enumTsBaseType(schema *openapi3.Schema) string {
	if schema == nil || schema.Type == nil {
		return "string"
	}
	switch {
	case schema.Type.Is("integer"), schema.Type.Is("number"):
		return "number"
	case schema.Type.Is("boolean"):
		return "boolean"
	default:
		return "string"
	}
}

// enumTsUnion renders an anonymous string/number literal union for an enum
// schema, e.g. `"success" | "error"`.
func enumTsUnion(schema *openapi3.Schema) string {
	if schema == nil || len(schema.Enum) == 0 {
		return "string"
	}
	isString := schema.Type == nil || schema.Type.Is("string")
	parts := make([]string, 0, len(schema.Enum))
	for _, v := range schema.Enum {
		if v == nil {
			parts = append(parts, "null")
		} else if isString {
			parts = append(parts, quoteTs(v))
		} else {
			parts = append(parts, sprintfTs(v))
		}
	}
	return strings.Join(parts, " | ")
}

// enumTsValue formats a single enum constant for use in generated code:
// string members are double-quoted, everything else stringified as-is.
func (tf *templateFuncs) enumTsValue(schema *openapi3.Schema, val any) string {
	if schema != nil && schema.Type != nil && !schema.Type.Is("string") {
		return sprintfTs(val)
	}
	return quoteTs(val)
}

// tsDoc renders a JSDoc comment block for the given text, indented by `indent`
// (a whitespace prefix applied to each line). Returns an empty string for empty
// input so templates can omit the block entirely.
func tsDoc(text string, indent string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	// A literal comment terminator in the source text would close the JSDoc
	// block early, spilling the remainder into TypeScript source; break it so
	// the generated comment stays valid.
	text = strings.ReplaceAll(text, "*/", "*\\/")
	wrapped := wordwrap.WrapString(text, 76)
	lines := strings.Split(wrapped, "\n")
	var b strings.Builder
	b.WriteString(indent)
	b.WriteString("/**\n")
	for _, line := range lines {
		b.WriteString(indent)
		b.WriteString(" * ")
		b.WriteString(strings.TrimRight(line, " "))
		b.WriteString("\n")
	}
	b.WriteString(indent)
	b.WriteString(" */")
	return b.String()
}

// quoteTs renders a value as a double-quoted TypeScript string literal, escaping
// backslashes, quotes, and control characters (newlines, carriage returns,
// tabs, etc.). JSON string syntax is a subset of TypeScript's, so the encoded
// result is always a valid TypeScript literal.
func quoteTs(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(sprintfTs(v)); err != nil {
		return `""`
	}
	return strings.TrimRight(b.String(), "\n")
}
