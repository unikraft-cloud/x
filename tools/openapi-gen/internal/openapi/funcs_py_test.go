// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package openapi

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/require"
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
			require.Equal(t, c.want, schemaToPyType(c.schema))
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
			require.Equal(t, c.want, schemaToPyType(c.schema))
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
		// Duplicate annotations collapse.
		{
			"anyOf duplicates",
			&openapi3.Schema{AnyOf: openapi3.SchemaRefs{
				{Value: &openapi3.Schema{Type: strType("string")}},
				{Value: &openapi3.Schema{Type: strType("string"), Format: "uuid"}},
			}},
			"str",
		},
		// An empty branch permits any value, so the union stays Any.
		{
			"anyOf with empty branch",
			&openapi3.Schema{AnyOf: openapi3.SchemaRefs{
				{Value: &openapi3.Schema{Type: strType("string")}},
				{Value: &openapi3.Schema{}},
			}},
			"Any",
		},
		// Composition wins over type: object.
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
			require.Equal(t, c.want, schemaToPyType(c.schema))
		})
	}
}

// pyTestParser builds a parser with an Empty schema that ParseModels skips.
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
		// The check must also reach nested references.
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
			require.Equal(t, c.want, schemaToPyTypeWithParser(c.schema, parser))
		})
	}
}

func TestParamToPyType(t *testing.T) {
	tf := &templateFuncs{parser: pyTestParser()}
	require.Equal(t, "Any", tf.paramToPyType(nil))
	// A parameter may legally omit `schema` (e.g. when `content` is used).
	require.Equal(t, "Any", tf.paramToPyType(&openapi3.Parameter{Name: "id"}))
	inline := &openapi3.Parameter{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: strType("integer")}}}
	require.Equal(t, "int", tf.paramToPyType(inline))
	// A $ref to a schema ParseModels skips must not name a class.
	empty := &openapi3.Parameter{Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/Empty"}}
	require.Equal(t, "Any", tf.paramToPyType(empty))
	model := &openapi3.Parameter{Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/Instance"}}
	require.Equal(t, "Instance", tf.paramToPyType(model))
}

func TestPyTypeRef(t *testing.T) {
	tf := &templateFuncs{parser: pyTestParser()}
	// A nil ref signals the no-content case so templates can emit None.
	require.Empty(t, tf.pyTypeRef(nil))
	require.Equal(t, "Any", tf.pyTypeRef(&openapi3.SchemaRef{Ref: "#/components/schemas/Empty"}))
	require.Equal(t, "Instance", tf.pyTypeRef(&openapi3.SchemaRef{Ref: "#/components/schemas/Instance"}))
	inline := &openapi3.SchemaRef{Value: &openapi3.Schema{Type: strType("boolean"), Nullable: true}}
	require.Equal(t, "bool | None", tf.pyTypeRef(inline))
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
		require.Equal(t, c.want, pySafeName(c.in), "pySafeName(%q)", c.in)
	}
}

func TestPyReservedWordsSorted(t *testing.T) {
	for i := 1; i < len(pyReservedWords); i++ {
		require.Less(t, pyReservedWords[i-1], pyReservedWords[i], "pyReservedWords not sorted at %d", i)
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
		// Go would emit true/false, which Python does not accept.
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
		// A member of a different runtime type is still rendered validly.
		{
			"string type with bool member",
			&openapi3.Schema{Type: strType("string"), Enum: []any{true}},
			"Literal[True]",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, enumPyLiteral(c.schema))
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
		require.Equal(t, c.want, tf.enumPyValue(c.schema, c.val), "enumPyValue(%v)", c.val)
	}
}

func TestQuotePyEscaping(t *testing.T) {
	// A literal newline would end the single-line Python literal.
	require.Equal(t, `"a\nb\tc"`, quotePy("a\nb\tc"))
	require.Equal(t, `"a\"b\\c"`, quotePy(`a"b\c`))
	require.Equal(t, `"a\rb"`, quotePy("a\rb"))
	require.Equal(t, `"plain"`, quotePy("plain"))
}

func TestPyDoc(t *testing.T) {
	require.Empty(t, pyDoc("", ""))
	require.Equal(t, `"""Creates an instance."""`, pyDoc("  Creates an instance.  ", ""))

	// An embedded triple quote would close the docstring early.
	got := pyDoc(`closes """ here`, "")
	require.NotContains(t, got, `""" here`, "pyDoc did not escape embedded triple quote")

	// A backslash is an escape sequence in a docstring, so it is doubled.
	require.Equal(t, `"""path C:\\new"""`, pyDoc(`path C:\new`, ""))

	// A trailing backslash would escape the first closing quote.
	require.Equal(t, `"""ends with \\"""`, pyDoc(`ends with \`, ""))

	// A trailing quote would make a fourth, so the multi-line form is used.
	got = pyDoc(`say "hi"`, "")
	require.True(t, strings.HasPrefix(got, "\"\"\"\n"), "pyDoc(trailing quote) = %q, want multi-line form", got)
	require.True(t, strings.HasSuffix(got, "\n\"\"\""), "pyDoc(trailing quote) = %q, want multi-line form", got)

	// Only content lines are indented: a blank indented line is trailing space.
	long := strings.Repeat("word ", 40)
	got = pyDoc(long, "    ")
	for line := range strings.SplitSeq(got, "\n") {
		if strings.TrimSpace(line) == "" {
			require.Empty(t, line, "pyDoc emitted a whitespace-only line: %q", got)
		}
		require.Equal(t, strings.TrimRight(line, " \t"), line, "pyDoc emitted trailing whitespace")
	}
	require.True(t, strings.HasSuffix(got, "\n    \"\"\""), "pyDoc(multi-line) did not close on its own indented line: %q", got)
}

func TestPyNullable(t *testing.T) {
	cases := []struct{ in, want string }{
		{"str", "str | None"},
		{"list[Instance]", "list[Instance] | None"},
		{"A | B", "A | B | None"},
		// Any already admits None; the empty no-content type stays empty.
		{"Any", "Any"},
		{"", ""},
		// Already optional: not widened twice.
		{"str | None", "str | None"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, pyNullable(c.in), "pyNullable(%q)", c.in)
	}
}
