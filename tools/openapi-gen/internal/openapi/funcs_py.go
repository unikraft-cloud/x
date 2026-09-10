// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package openapi

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	wordwrap "github.com/mitchellh/go-wordwrap"
)

// pyReservedWords are Python keywords that cannot be used as bare identifiers.
// A colliding name gets a trailing underscore (the PEP 8 convention, e.g.
// `from` -> `from_`). Kept sorted for binary search.
var pyReservedWords = []string{
	"False",
	"None",
	"True",
	"and",
	"as",
	"assert",
	"async",
	"await",
	"break",
	"class",
	"continue",
	"def",
	"del",
	"elif",
	"else",
	"except",
	"finally",
	"for",
	"from",
	"global",
	"if",
	"import",
	"in",
	"is",
	"lambda",
	"nonlocal",
	"not",
	"or",
	"pass",
	"raise",
	"return",
	"try",
	"while",
	"with",
	"yield",
}

// pySafeName suffixes a Python keyword with "_" so it can be used as an
// identifier (parameter name, field name). Mirrors goSafeName/tsSafeName but
// uses a trailing underscore per Python convention.
func pySafeName(s string) string {
	if _, found := slices.BinarySearch(pyReservedWords, s); found {
		return s + "_"
	}
	return s
}

// schemaToPyType converts an OpenAPI schema to a Python type annotation string.
func (tf *templateFuncs) schemaToPyType(schema *openapi3.Schema) string {
	return schemaToPyTypeWithParser(schema, tf.parser)
}

// paramToPyType converts an OpenAPI parameter to a Python type annotation.
func (tf *templateFuncs) paramToPyType(param *openapi3.Parameter) string {
	if param == nil {
		return "Any"
	}
	// A $ref parameter schema resolves to the referenced type name, collapsing
	// to `Any` when that component is an empty (skipped) schema.
	if param.Schema != nil && param.Schema.Ref != "" {
		return refPyName(tf.parser, param.Schema.Ref)
	}
	// Parameters may legally omit `schema` (e.g. when `content` is used).
	if param.Schema == nil {
		return "Any"
	}
	return schemaToPyTypeWithParser(param.Schema.Value, tf.parser)
}

// pyTypeRef renders a *openapi3.SchemaRef as a Python type: a $ref becomes the
// referenced type name, an inline schema is mapped structurally. Returns "" for
// a nil ref so templates can detect the no-content (None) case.
func (tf *templateFuncs) pyTypeRef(ref *openapi3.SchemaRef) string {
	if ref == nil {
		return ""
	}
	if ref.Ref != "" {
		return refPyName(tf.parser, ref.Ref)
	}
	return schemaToPyTypeWithParser(ref.Value, tf.parser)
}

// schemaToPyType converts an OpenAPI schema to a Python type annotation string.
// It is a thin wrapper around schemaToPyTypeWithParser without a parser, so
// references to empty (skipped) schemas are not collapsed to `Any`.
func schemaToPyType(schema *openapi3.Schema) string {
	return schemaToPyTypeWithParser(schema, nil)
}

// schemaToPyTypeWithParser converts an OpenAPI schema to a Python type
// annotation. When parser is non-nil, references to empty schemas (which
// ParseModels omits) collapse to `Any` so generated code never names a class
// that is never emitted. The parser-aware check is applied recursively to
// nested references (array items, additionalProperties, composition branches).
// `nullable: true` schemas are widened with `| None`.
func schemaToPyTypeWithParser(schema *openapi3.Schema, parser *Parser) string {
	if schema == nil {
		return "Any"
	}
	t := basePyType(schema, parser)
	if schema.Nullable {
		return pyNullable(t)
	}
	return t
}

// basePyType maps a schema to its Python type annotation, ignoring nullability
// (handled by schemaToPyTypeWithParser).
func basePyType(schema *openapi3.Schema, parser *Parser) string {
	// allOf with a single $ref is the common type-alias pattern.
	if len(schema.AllOf) == 1 && schema.AllOf[0].Ref != "" {
		return refPyName(parser, schema.AllOf[0].Ref)
	}

	// Arrays.
	if schema.Type.Is("array") {
		if schema.Items != nil && schema.Items.Ref != "" {
			return "list[" + refPyName(parser, schema.Items.Ref) + "]"
		}
		if schema.Items != nil {
			return "list[" + schemaToPyTypeWithParser(schema.Items.Value, parser) + "]"
		}
		return "list[Any]"
	}

	// Named inline enums surface as their generated Literal alias; otherwise
	// fall back to an anonymous Literal[...] union.
	if len(schema.Enum) > 0 {
		if schema.Title != "" {
			return schema.Title
		}
		return enumPyLiteral(schema)
	}

	// Composition renders as a union even when `type: object` is present, so
	// object-composed schemas don't collapse to generic dicts. (Single-$ref
	// allOf is handled above.)
	if c := pyComposite(schema, parser); c != "" {
		return c
	}

	// Objects and maps.
	if schema.Type.Is("object") {
		if schema.AdditionalProperties.Schema != nil {
			if schema.AdditionalProperties.Schema.Ref != "" {
				return "dict[str, " + refPyName(parser, schema.AdditionalProperties.Schema.Ref) + "]"
			}
			return "dict[str, " + schemaToPyTypeWithParser(schema.AdditionalProperties.Schema.Value, parser) + "]"
		}
		if schema.AdditionalProperties.Has != nil && *schema.AdditionalProperties.Has {
			return "dict[str, Any]"
		}
		if len(schema.Properties) > 0 && schema.Title != "" {
			return schema.Title
		}
		return "dict[str, Any]"
	}

	if schema.Type == nil {
		return "Any"
	}

	switch {
	case schema.Type.Is("string"):
		// date/date-time carried as ISO-8601 strings over JSON.
		return "str"
	case schema.Type.Is("integer"):
		return "int"
	case schema.Type.Is("number"):
		return "float"
	case schema.Type.Is("boolean"):
		return "bool"
	}

	return "Any"
}

// pyComposite renders oneOf/anyOf branches as a Python union, e.g.
// `str | ImageSpec`. Python has no intersection type, so a multi-entry allOf
// (an object satisfying every branch at once) is not expressible and degrades
// to `Any`. Returns "" when the schema carries no such composition.
func pyComposite(schema *openapi3.Schema, parser *Parser) string {
	switch {
	case len(schema.OneOf) > 0:
		return pyUnion(schema.OneOf, parser)
	case len(schema.AnyOf) > 0:
		return pyUnion(schema.AnyOf, parser)
	case len(schema.AllOf) > 0:
		return "Any"
	}
	return ""
}

// pyUnion joins branches with `|`, de-duplicating repeated annotations. A
// branch that maps to `Any` permits arbitrary values, so it widens the whole
// union to `Any` rather than being dropped (which would narrow the annotation
// and reject values the API accepts).
func pyUnion(branches openapi3.SchemaRefs, parser *Parser) string {
	var parts []string
	for _, branch := range branches {
		if branch == nil {
			continue
		}

		var py string
		if branch.Ref != "" {
			py = refPyName(parser, branch.Ref)
		} else {
			py = schemaToPyTypeWithParser(branch.Value, parser)
		}

		if py == "Any" {
			return "Any"
		}
		if !slices.Contains(parts, py) {
			parts = append(parts, py)
		}
	}

	if len(parts) == 0 {
		return "Any"
	}

	return strings.Join(parts, " | ")
}

// refPyName returns the Python type name for a $ref string. When parser is
// non-nil and the referenced component is an empty schema (omitted by
// ParseModels), it returns "Any" so generated code never names a class that is
// never emitted.
func refPyName(parser *Parser, ref string) string {
	name := extractTypeFromRef(ref)
	if parser != nil && parser.doc != nil && parser.doc.Components != nil {
		if sr, ok := parser.doc.Components.Schemas[name]; ok {
			if schemaIsEmpty(sr.Value) {
				return "Any"
			}
		}
	}
	return name
}

// pyNullable widens a type annotation with `| None`. `Any` already admits None,
// and an annotation that is already optional is left alone.
func pyNullable(t string) string {
	if t == "" || t == "Any" {
		return t
	}
	for part := range strings.SplitSeq(t, "|") {
		if strings.TrimSpace(part) == "None" {
			return t
		}
	}
	return t + " | None"
}

// enumPyLiteral renders an enum schema as a typing.Literal[...] alias, e.g.
// `Literal["success", "error"]`.
func enumPyLiteral(schema *openapi3.Schema) string {
	if schema == nil || len(schema.Enum) == 0 {
		return "str"
	}
	parts := make([]string, 0, len(schema.Enum))
	for _, v := range schema.Enum {
		parts = append(parts, pyLiteral(v))
	}
	return "Literal[" + strings.Join(parts, ", ") + "]"
}

// enumPyValue formats a single enum constant for use in generated code. The
// schema is accepted for symmetry with enumTsValue/enumGoValue but unused: the
// member's own runtime type decides its rendering.
func (tf *templateFuncs) enumPyValue(_ *openapi3.Schema, val any) string {
	return pyLiteral(val)
}

// pyLiteral renders an enum member as a Python literal, dispatching on the
// value's runtime JSON type: Go's default formatting would emit `true`/`false`
// for booleans and `<nil>` for null, none of which are valid Python.
func pyLiteral(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case bool:
		if t {
			return "True"
		}
		return "False"
	case string:
		return quotePy(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// pyDoc renders a triple-quoted Python docstring for the given text, indented by
// `indent`. Returns an empty string for empty input so templates can omit it.
func pyDoc(text string, indent string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	// Backslashes are escape sequences inside a docstring, so an unescaped one
	// from the source text would either be swallowed or reinterpreted (`\n`
	// becoming a newline). Escape them before the triple-quote guard below, so
	// the backslashes that guard introduces are not themselves escaped.
	text = strings.ReplaceAll(text, `\`, `\\`)
	// Escape any embedded triple-quote so the docstring stays well-formed.
	text = strings.ReplaceAll(text, `"""`, `\"\"\"`)
	wrapped := wordwrap.WrapString(text, 76)
	lines := strings.Split(wrapped, "\n")
	// A single line closes on the same line, so text ending in `"` would abut
	// the closing quotes and produce a fourth. Fall through to the multi-line
	// form, which closes on its own line, rather than escaping the quote.
	if len(lines) == 1 && !strings.HasSuffix(strings.TrimRight(lines[0], " "), `"`) {
		return `"""` + strings.TrimRight(lines[0], " ") + `"""`
	}
	var b strings.Builder
	b.WriteString(`"""`)
	b.WriteString("\n")
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		// Indent only lines that have content: an indented blank line is
		// trailing whitespace, which every Python linter reports.
		if trimmed != "" {
			b.WriteString(indent)
			b.WriteString(trimmed)
		}
		b.WriteString("\n")
	}
	b.WriteString(indent)
	b.WriteString(`"""`)
	return b.String()
}

// quotePy renders a value as a double-quoted Python string literal, escaping
// backslashes, quotes, and control characters (newlines, carriage returns,
// tabs, etc.). JSON string syntax is a subset of Python's, so the encoded
// result is always a valid Python literal.
func quotePy(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(fmt.Sprintf("%v", v)); err != nil {
		return `""`
	}
	return strings.TrimRight(b.String(), "\n")
}
