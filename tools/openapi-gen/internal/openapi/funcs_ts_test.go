// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package openapi

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/require"
)

func strType(t string) *openapi3.Types {
	tt := openapi3.Types([]string{t})
	return &tt
}

func TestSchemaToTsType(t *testing.T) {
	cases := []struct {
		name   string
		schema *openapi3.Schema
		want   string
	}{
		{"nil", nil, "unknown"},
		{"string", &openapi3.Schema{Type: strType("string")}, "string"},
		{"datetime", &openapi3.Schema{Type: strType("string"), Format: "date-time"}, "string"},
		{"integer", &openapi3.Schema{Type: strType("integer"), Format: "int64"}, "number"},
		{"number", &openapi3.Schema{Type: strType("number"), Format: "float"}, "number"},
		{"boolean", &openapi3.Schema{Type: strType("boolean")}, "boolean"},
		{
			"array of string",
			&openapi3.Schema{Type: strType("array"), Items: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: strType("string")}}},
			"string[]",
		},
		{
			"array of ref",
			&openapi3.Schema{Type: strType("array"), Items: &openapi3.SchemaRef{Ref: "#/components/schemas/Instance"}},
			"Instance[]",
		},
		{
			"map of string",
			&openapi3.Schema{Type: strType("object"), AdditionalProperties: openapi3.AdditionalProperties{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: strType("string")}}}},
			"Record<string, string>",
		},
		{
			"allOf single ref alias",
			&openapi3.Schema{AllOf: openapi3.SchemaRefs{{Ref: "#/components/schemas/ImageSpec"}}},
			"ImageSpec",
		},
		{
			"string enum union",
			&openapi3.Schema{Type: strType("string"), Enum: []any{"success", "error"}},
			`"success" | "error"`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, schemaToTsType(c.schema))
		})
	}
}

func TestTsSafeName(t *testing.T) {
	require.Equal(t, "_interface", tsSafeName("interface"))
	require.Equal(t, "Instance", tsSafeName("Instance"))
	// `constructor` must be escaped so an operation ID normalising to it is not
	// emitted as the class constructor.
	require.Equal(t, "_constructor", tsSafeName("constructor"))
}

func TestSchemaToTsTypeNullable(t *testing.T) {
	cases := []struct {
		name   string
		schema *openapi3.Schema
		want   string
	}{
		{"nullable string", &openapi3.Schema{Type: strType("string"), Nullable: true}, "string | null"},
		{
			"nullable array",
			&openapi3.Schema{Type: strType("array"), Nullable: true, Items: &openapi3.SchemaRef{Ref: "#/components/schemas/Instance"}},
			"Instance[] | null",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, schemaToTsType(c.schema))
		})
	}
}

func TestSchemaToTsTypeComposition(t *testing.T) {
	oneOf := &openapi3.Schema{OneOf: openapi3.SchemaRefs{
		{Ref: "#/components/schemas/A"},
		{Ref: "#/components/schemas/B"},
	}}
	require.Equal(t, "A | B", schemaToTsType(oneOf))
	anyOf := &openapi3.Schema{AnyOf: openapi3.SchemaRefs{
		{Value: &openapi3.Schema{Type: strType("string")}},
		{Ref: "#/components/schemas/B"},
	}}
	require.Equal(t, "string | B", schemaToTsType(anyOf))
	allOf := &openapi3.Schema{AllOf: openapi3.SchemaRefs{
		{Ref: "#/components/schemas/A"},
		{Ref: "#/components/schemas/B"},
	}}
	require.Equal(t, "A & B", schemaToTsType(allOf))

	itemAllOfObject := &openapi3.Schema{
		Type: strType("array"),
		Items: &openapi3.SchemaRef{Value: &openapi3.Schema{
			Type: strType("object"),
			AllOf: openapi3.SchemaRefs{
				{Ref: "#/components/schemas/A"},
				{Ref: "#/components/schemas/B"},
			},
		}},
	}
	require.Equal(t, "(A & B)[]", schemaToTsType(itemAllOfObject))
}

func TestQualifyModels(t *testing.T) {
	models := []Model{{SchemaName: "Instance"}, {SchemaName: "Volume"}}
	tf := &templateFuncs{models: &models}
	cases := []struct {
		in, want string
	}{
		{"Instance[]", "models.Instance[]"},
		{"Record<string, Volume>", "Record<string, models.Volume>"},
		{"string", "string"},
		// Already-qualified names are not re-qualified.
		{"models.Instance", "models.Instance"},
		// Model-like text inside string literals is left untouched.
		{`"Instance" | "other"`, `"Instance" | "other"`},
		{"(Instance | Volume)[]", "(models.Instance | models.Volume)[]"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			require.Equal(t, c.want, tf.qualifyModels("models", c.in))
		})
	}
}

func TestQuoteTsControlChars(t *testing.T) {
	require.Equal(t, `"a\nb\tc"`, quoteTs("a\nb\tc"))
	require.Equal(t, `"a\"b\\c"`, quoteTs(`a"b\c`))
}

func TestTsDocEscapesTerminator(t *testing.T) {
	got := tsDoc("closes */ here", "")
	require.NotContains(t, got, "*/ here")
}

func TestTsArrayOfUnion(t *testing.T) {
	require.Equal(t, "(A | B)[]", tsArrayOf("A | B"))
	require.Equal(t, "Instance[]", tsArrayOf("Instance"))
}

func TestReservedWordsSorted(t *testing.T) {
	require.IsIncreasing(t, tsReservedWords)
}
