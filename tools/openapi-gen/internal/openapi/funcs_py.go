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

// pyReservedWords lists the Python keywords. Keep it sorted for binary search.
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

// pySafeName adds a trailing "_" to a Python keyword, e.g. `from` -> `from_`.
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
	// A $ref resolves to the type name, or to Any if the component is empty.
	if param.Schema != nil && param.Schema.Ref != "" {
		return refPyName(tf.parser, param.Schema.Ref)
	}
	// Parameters may legally omit `schema` (e.g. when `content` is used).
	if param.Schema == nil {
		return "Any"
	}
	return schemaToPyTypeWithParser(param.Schema.Value, tf.parser)
}

// pyTypeRef renders a SchemaRef as a Python type. A nil ref returns "" so
// templates can detect the no-content case.
func (tf *templateFuncs) pyTypeRef(ref *openapi3.SchemaRef) string {
	if ref == nil {
		return ""
	}
	if ref.Ref != "" {
		return refPyName(tf.parser, ref.Ref)
	}
	return schemaToPyTypeWithParser(ref.Value, tf.parser)
}

// schemaToPyType converts a schema to a Python type without a parser, so
// references to empty schemas are not collapsed to Any.
func schemaToPyType(schema *openapi3.Schema) string {
	return schemaToPyTypeWithParser(schema, nil)
}

// schemaToPyTypeWithParser converts a schema to a Python type. With a parser,
// references to empty (omitted) schemas collapse to Any. Nullable adds | None.
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

// basePyType maps a schema to a Python type, ignoring nullability.
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

	// A titled enum uses its Literal alias; otherwise render an inline Literal.
	if len(schema.Enum) > 0 {
		if schema.Title != "" {
			return schema.Title
		}
		return enumPyLiteral(schema)
	}

	// Composition wins over type: object, so it does not collapse to a dict.
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

// pyComposite renders oneOf/anyOf as a union. Multi-entry allOf has no Python
// equivalent and becomes Any. Returns "" when there is no composition.
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

// pyUnion joins unique branches with `|`. An Any branch permits any value,
// so it widens the whole union to Any.
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

// refPyName returns the type name for a $ref. With a parser, a reference to
// an empty (omitted) schema returns Any so no missing class is named.
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

// pyNullable adds `| None` unless the type is Any, empty, or already optional.
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

// enumPyLiteral renders an enum schema as Literal[...], e.g. Literal["a", "b"].
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

// enumPyValue formats one enum member as a Python literal. The schema is
// unused; it exists for symmetry with enumTsValue and enumGoValue.
func (tf *templateFuncs) enumPyValue(_ *openapi3.Schema, val any) string {
	return pyLiteral(val)
}

// pyLiteral renders a value as a Python literal by its runtime type, because
// Go's `true`, `false` and `<nil>` are not valid Python.
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

// pyDoc renders text as an indented triple-quoted docstring, or "" for no text.
func pyDoc(text string, indent string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	// Double backslashes first, so the guard below is not escaped again.
	text = strings.ReplaceAll(text, `\`, `\\`)
	// Escape any embedded triple-quote so the docstring stays well-formed.
	text = strings.ReplaceAll(text, `"""`, `\"\"\"`)
	wrapped := wordwrap.WrapString(text, 76)
	lines := strings.Split(wrapped, "\n")
	// Text that ends in `"` would abut the closing quotes and make a fourth,
	// so it uses the multi-line form.
	if len(lines) == 1 && !strings.HasSuffix(strings.TrimRight(lines[0], " "), `"`) {
		return `"""` + strings.TrimRight(lines[0], " ") + `"""`
	}
	var b strings.Builder
	b.WriteString(`"""`)
	b.WriteString("\n")
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		// Do not indent blank lines: linters report the trailing whitespace.
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

// quotePy renders a value as a double-quoted Python string. JSON string syntax
// is a subset of Python's, so the JSON encoding is a valid literal.
func quotePy(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(fmt.Sprintf("%v", v)); err != nil {
		return `""`
	}
	return strings.TrimRight(b.String(), "\n")
}
