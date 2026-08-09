// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package openapi

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestSchemaToPyType(t *testing.T) {
	cases := []struct {
		name   string
		schema *openapi3.Schema
		want   string
	}{
		{"nil", nil, "Any"},
		{"no type", &openapi3.Schema{}, "Any"},
		{"string", &openapi3.Schema{Type: strType("string")}, "str"},
		{"datetime", &openapi3.Schema{Type: strType("string"), Format: "date-time"}, "str"},
		{"integer", &openapi3.Schema{Type: strType("integer"), Format: "int64"}, "int"},
		{"number", &openapi3.Schema{Type: strType("number"), Format: "float"}, "float"},
		{"boolean", &openapi3.Schema{Type: strType("boolean")}, "bool"},
		{
			"array of string",
			&openapi3.Schema{Type: strType("array"), Items: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: strType("string")}}},
			"list[str]",
		},
		{
			"array of ref",
			&openapi3.Schema{Type: strType("array"), Items: &openapi3.SchemaRef{Ref: "#/components/schemas/Instance"}},
			"list[Instance]",
		},
		{"array without items", &openapi3.Schema{Type: strType("array")}, "list[Any]"},
		{
			"map of string",
			&openapi3.Schema{Type: strType("object"), AdditionalProperties: openapi3.AdditionalProperties{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: strType("string")}}}},
			"dict[str, str]",
		},
		{
			"map of ref",
			&openapi3.Schema{Type: strType("object"), AdditionalProperties: openapi3.AdditionalProperties{Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/Volume"}}},
			"dict[str, Volume]",
		},
		{"bare object", &openapi3.Schema{Type: strType("object")}, "dict[str, Any]"},
		{
			"titled object",
			&openapi3.Schema{
				Type:       strType("object"),
				Title:      "Instance",
				Properties: openapi3.Schemas{"name": {Value: &openapi3.Schema{Type: strType("string")}}},
			},
			"Instance",
		},
		{
			"allOf single ref alias",
			&openapi3.Schema{AllOf: openapi3.SchemaRefs{{Ref: "#/components/schemas/ImageSpec"}}},
			"ImageSpec",
		},
		{
			"string enum literal",
			&openapi3.Schema{Type: strType("string"), Enum: []any{"success", "error"}},
			`Literal["success", "error"]`,
		},
		{
			"titled enum alias",
			&openapi3.Schema{Type: strType("string"), Title: "State", Enum: []any{"a", "b"}},
			"State",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := schemaToPyType(c.schema); got != c.want {
				t.Fatalf("schemaToPyType() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSchemaToPyTypeNullable(t *testing.T) {
	cases := []struct {
		name   string
		schema *openapi3.Schema
		want   string
	}{
		{"nullable string", &openapi3.Schema{Type: strType("string"), Nullable: true}, "str | None"},
		{
			"nullable array of ref",
			&openapi3.Schema{Type: strType("array"), Nullable: true, Items: &openapi3.SchemaRef{Ref: "#/components/schemas/Instance"}},
			"list[Instance] | None",
		},
		{
			"nullable alias",
			&openapi3.Schema{Nullable: true, AllOf: openapi3.SchemaRefs{{Ref: "#/components/schemas/ImageSpec"}}},
			"ImageSpec | None",
		},
		{
			"nullable union",
			&openapi3.Schema{Nullable: true, OneOf: openapi3.SchemaRefs{
				{Ref: "#/components/schemas/A"},
				{Ref: "#/components/schemas/B"},
			}},
			"A | B | None",
		},
		{
			"nullable map",
			&openapi3.Schema{Type: strType("object"), Nullable: true, AdditionalProperties: openapi3.AdditionalProperties{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: strType("string")}}}},
			"dict[str, str] | None",
		},
		// Any already admits None, so it is not widened.
		{"nullable untyped", &openapi3.Schema{Nullable: true}, "Any"},
		// Nullability is applied recursively to nested schemas.
		{
			"array of nullable string",
			&openapi3.Schema{Type: strType("array"), Items: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: strType("string"), Nullable: true}}},
			"list[str | None]",
		},
		{
			"map of nullable int",
			&openapi3.Schema{Type: strType("object"), AdditionalProperties: openapi3.AdditionalProperties{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: strType("integer"), Nullable: true}}}},
			"dict[str, int | None]",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := schemaToPyType(c.schema); got != c.want {
				t.Fatalf("schemaToPyType() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSchemaToPyTypeComposition(t *testing.T) {
	cases := []struct {
		name   string
		schema *openapi3.Schema
		want   string
	}{
		{
			"oneOf refs",
			&openapi3.Schema{OneOf: openapi3.SchemaRefs{
				{Ref: "#/components/schemas/A"},
				{Ref: "#/components/schemas/B"},
			}},
			"A | B",
		},
		{
			"anyOf mixed",
			&openapi3.Schema{AnyOf: openapi3.SchemaRefs{
				{Value: &openapi3.Schema{Type: strType("string")}},
				{Ref: "#/components/schemas/B"},
			}},
			"str | B",
		},
		// Duplicate annotations collapse (e.g. integer and number both mapping
		// to a single Python type would otherwise repeat).
		{
			"anyOf duplicates",
			&openapi3.Schema{AnyOf: openapi3.SchemaRefs{
				{Value: &openapi3.Schema{Type: strType("string")}},
				{Value: &openapi3.Schema{Type: strType("string"), Format: "uuid"}},
			}},
			"str",
		},
		// An empty branch permits any value, so the union must stay Any rather
		// than narrowing to the typed branch.
		{
			"anyOf with empty branch",
			&openapi3.Schema{AnyOf: openapi3.SchemaRefs{
				{Value: &openapi3.Schema{Type: strType("string")}},
				{Value: &openapi3.Schema{}},
			}},
			"Any",
		},
		// Composition wins over `type: object`, so a composed schema does not
		// collapse to a generic dict.
		{
			"object with oneOf",
			&openapi3.Schema{Type: strType("object"), OneOf: openapi3.SchemaRefs{
				{Ref: "#/components/schemas/A"},
				{Ref: "#/components/schemas/B"},
			}},
			"A | B",
		},
		// Python has no intersection type.
		{
			"multi allOf",
			&openapi3.Schema{AllOf: openapi3.SchemaRefs{
				{Ref: "#/components/schemas/A"},
				{Ref: "#/components/schemas/B"},
			}},
			"Any",
		},
		{
			"array of union",
			&openapi3.Schema{Type: strType("array"), Items: &openapi3.SchemaRef{Value: &openapi3.Schema{
				OneOf: openapi3.SchemaRefs{
					{Ref: "#/components/schemas/A"},
					{Ref: "#/components/schemas/B"},
				},
			}}},
			"list[A | B]",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := schemaToPyType(c.schema); got != c.want {
				t.Fatalf("schemaToPyType() = %q, want %q", got, c.want)
			}
		})
	}
}

// pyTestParser builds a parser whose document declares Empty as a schema that
// ParseModels skips, so references to it must not name a generated class.
func pyTestParser() *Parser {
	return &Parser{doc: &openapi3.T{Components: &openapi3.Components{
		Schemas: openapi3.Schemas{
			"Empty":    {Value: &openapi3.Schema{}},
			"Instance": {Value: &openapi3.Schema{Type: strType("object"), Properties: openapi3.Schemas{"name": {Value: &openapi3.Schema{Type: strType("string")}}}}},
		},
	}}}
}

func TestSchemaToPyTypeEmptyRefs(t *testing.T) {
	parser := pyTestParser()
	cases := []struct {
		name   string
		schema *openapi3.Schema
		want   string
	}{
		{
			"alias to empty",
			&openapi3.Schema{AllOf: openapi3.SchemaRefs{{Ref: "#/components/schemas/Empty"}}},
			"Any",
		},
		{
			"alias to model",
			&openapi3.Schema{AllOf: openapi3.SchemaRefs{{Ref: "#/components/schemas/Instance"}}},
			"Instance",
		},
		// The parser-aware check must reach nested references too, not just a
		// top-level single-ref allOf.
		{
			"array of empty",
			&openapi3.Schema{Type: strType("array"), Items: &openapi3.SchemaRef{Ref: "#/components/schemas/Empty"}},
			"list[Any]",
		},
		{
			"map of empty",
			&openapi3.Schema{Type: strType("object"), AdditionalProperties: openapi3.AdditionalProperties{Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/Empty"}}},
			"dict[str, Any]",
		},
		{
			"union with empty branch",
			&openapi3.Schema{OneOf: openapi3.SchemaRefs{
				{Ref: "#/components/schemas/Instance"},
				{Ref: "#/components/schemas/Empty"},
			}},
			"Any",
		},
		{
			"unknown ref is kept",
			&openapi3.Schema{Type: strType("array"), Items: &openapi3.SchemaRef{Ref: "#/components/schemas/Other"}},
			"list[Other]",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := schemaToPyTypeWithParser(c.schema, parser); got != c.want {
				t.Fatalf("schemaToPyTypeWithParser() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestParamToPyType(t *testing.T) {
	tf := &templateFuncs{parser: pyTestParser()}
	if got := tf.paramToPyType(nil); got != "Any" {
		t.Fatalf("paramToPyType(nil) = %q, want %q", got, "Any")
	}
	// A parameter may legally omit `schema` (e.g. when `content` is used).
	if got := tf.paramToPyType(&openapi3.Parameter{Name: "id"}); got != "Any" {
		t.Fatalf("paramToPyType(no schema) = %q, want %q", got, "Any")
	}
	inline := &openapi3.Parameter{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: strType("integer")}}}
	if got := tf.paramToPyType(inline); got != "int" {
		t.Fatalf("paramToPyType(integer) = %q, want %q", got, "int")
	}
	// A $ref to a schema ParseModels skips must not name a class.
	empty := &openapi3.Parameter{Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/Empty"}}
	if got := tf.paramToPyType(empty); got != "Any" {
		t.Fatalf("paramToPyType(empty ref) = %q, want %q", got, "Any")
	}
	model := &openapi3.Parameter{Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/Instance"}}
	if got := tf.paramToPyType(model); got != "Instance" {
		t.Fatalf("paramToPyType(model ref) = %q, want %q", got, "Instance")
	}
}

func TestPyTypeRef(t *testing.T) {
	tf := &templateFuncs{parser: pyTestParser()}
	// A nil ref signals the no-content case so templates can emit None.
	if got := tf.pyTypeRef(nil); got != "" {
		t.Fatalf("pyTypeRef(nil) = %q, want %q", got, "")
	}
	if got := tf.pyTypeRef(&openapi3.SchemaRef{Ref: "#/components/schemas/Empty"}); got != "Any" {
		t.Fatalf("pyTypeRef(empty ref) = %q, want %q", got, "Any")
	}
	if got := tf.pyTypeRef(&openapi3.SchemaRef{Ref: "#/components/schemas/Instance"}); got != "Instance" {
		t.Fatalf("pyTypeRef(model ref) = %q, want %q", got, "Instance")
	}
	inline := &openapi3.SchemaRef{Value: &openapi3.Schema{Type: strType("boolean"), Nullable: true}}
	if got := tf.pyTypeRef(inline); got != "bool | None" {
		t.Fatalf("pyTypeRef(inline) = %q, want %q", got, "bool | None")
	}
}

func TestPySafeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"from", "from_"},
		{"lambda", "lambda_"},
		{"None", "None_"},
		{"async", "async_"},
		// Not keywords: left alone.
		{"Instance", "Instance"},
		{"name", "name"},
		{"print", "print"},
	}
	for _, c := range cases {
		if got := pySafeName(c.in); got != c.want {
			t.Fatalf("pySafeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPyReservedWordsSorted(t *testing.T) {
	for i := 1; i < len(pyReservedWords); i++ {
		if pyReservedWords[i-1] >= pyReservedWords[i] {
			t.Fatalf("pyReservedWords not sorted at %d: %q >= %q", i, pyReservedWords[i-1], pyReservedWords[i])
		}
	}
}

func TestEnumPyLiteral(t *testing.T) {
	cases := []struct {
		name   string
		schema *openapi3.Schema
		want   string
	}{
		{"nil", nil, "str"},
		{"no members", &openapi3.Schema{Type: strType("string")}, "str"},
		{
			"strings",
			&openapi3.Schema{Type: strType("string"), Enum: []any{"success", "error"}},
			`Literal["success", "error"]`,
		},
		{
			"integers",
			&openapi3.Schema{Type: strType("integer"), Enum: []any{int64(1), int64(2)}},
			"Literal[1, 2]",
		},
		// Go's default formatting would emit `true`/`false`, which Python does
		// not accept.
		{
			"booleans",
			&openapi3.Schema{Type: strType("boolean"), Enum: []any{true, false}},
			"Literal[True, False]",
		},
		// ... and `<nil>` for a null member.
		{
			"null member",
			&openapi3.Schema{Type: strType("string"), Enum: []any{"a", nil}},
			`Literal["a", None]`,
		},
		// A member whose runtime type disagrees with the declared type is still
		// rendered validly.
		{
			"string type with bool member",
			&openapi3.Schema{Type: strType("string"), Enum: []any{true}},
			"Literal[True]",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := enumPyLiteral(c.schema); got != c.want {
				t.Fatalf("enumPyLiteral() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestEnumPyValue(t *testing.T) {
	tf := &templateFuncs{}
	strSchema := &openapi3.Schema{Type: strType("string")}
	boolSchema := &openapi3.Schema{Type: strType("boolean")}
	cases := []struct {
		schema *openapi3.Schema
		val    any
		want   string
	}{
		{strSchema, "success", `"success"`},
		{boolSchema, true, "True"},
		{boolSchema, false, "False"},
		{strSchema, nil, "None"},
		{&openapi3.Schema{Type: strType("integer")}, int64(7), "7"},
		{nil, "a", `"a"`},
	}
	for _, c := range cases {
		if got := tf.enumPyValue(c.schema, c.val); got != c.want {
			t.Fatalf("enumPyValue(%v) = %q, want %q", c.val, got, c.want)
		}
	}
}

func TestQuotePyEscaping(t *testing.T) {
	// Control characters must be escaped: a literal newline would end the
	// generated single-line Python literal.
	if got := quotePy("a\nb\tc"); got != `"a\nb\tc"` {
		t.Fatalf("quotePy(control) = %q", got)
	}
	if got := quotePy(`a"b\c`); got != `"a\"b\\c"` {
		t.Fatalf("quotePy(quotes) = %q", got)
	}
	if got := quotePy("a\rb"); got != `"a\rb"` {
		t.Fatalf("quotePy(carriage return) = %q", got)
	}
	if got := quotePy("plain"); got != `"plain"` {
		t.Fatalf("quotePy(plain) = %q", got)
	}
}

func TestPyDoc(t *testing.T) {
	if got := pyDoc("", ""); got != "" {
		t.Fatalf("pyDoc(empty) = %q, want %q", got, "")
	}
	if got := pyDoc("  Creates an instance.  ", ""); got != `"""Creates an instance."""` {
		t.Fatalf("pyDoc(single line) = %q", got)
	}

	// An embedded triple quote would close the docstring early.
	got := pyDoc(`closes """ here`, "")
	if strings.Contains(got, `""" here`) {
		t.Fatalf("pyDoc did not escape embedded triple quote: %q", got)
	}

	// A backslash in the source text is an escape sequence inside a docstring,
	// so it must be doubled to survive as a literal backslash.
	got = pyDoc(`path C:\new`, "")
	if got != `"""path C:\\new"""` {
		t.Fatalf("pyDoc(backslash) = %q, want %q", got, `"""path C:\\new"""`)
	}

	// A trailing backslash would otherwise escape the first closing quote and
	// leave the docstring unterminated.
	got = pyDoc(`ends with \`, "")
	if got != `"""ends with \\"""` {
		t.Fatalf("pyDoc(trailing backslash) = %q, want %q", got, `"""ends with \\"""`)
	}

	// A trailing quote would abut the closing quotes and produce a fourth, so
	// the multi-line form is used instead.
	got = pyDoc(`say "hi"`, "")
	if !strings.HasPrefix(got, "\"\"\"\n") || !strings.HasSuffix(got, "\n\"\"\"") {
		t.Fatalf("pyDoc(trailing quote) = %q, want multi-line form", got)
	}

	// Multi-line output indents content lines only: an indented blank line is
	// trailing whitespace, which Python linters report.
	long := strings.Repeat("word ", 40)
	got = pyDoc(long, "    ")
	for line := range strings.SplitSeq(got, "\n") {
		if strings.TrimSpace(line) == "" && line != "" {
			t.Fatalf("pyDoc emitted a whitespace-only line: %q", got)
		}
		if strings.TrimRight(line, " \t") != line {
			t.Fatalf("pyDoc emitted trailing whitespace: %q", line)
		}
	}
	if !strings.HasSuffix(got, "\n    \"\"\"") {
		t.Fatalf("pyDoc(multi-line) did not close on its own indented line: %q", got)
	}
}

func TestPyNullable(t *testing.T) {
	cases := []struct{ in, want string }{
		{"str", "str | None"},
		{"list[Instance]", "list[Instance] | None"},
		{"A | B", "A | B | None"},
		// Any already admits None; an empty type stays empty (the no-content
		// signal from pyTypeRef).
		{"Any", "Any"},
		{"", ""},
		// Already optional: not widened twice.
		{"str | None", "str | None"},
	}
	for _, c := range cases {
		if got := pyNullable(c.in); got != c.want {
			t.Fatalf("pyNullable(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
