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
			if got := schemaToTsType(c.schema); got != c.want {
				t.Fatalf("schemaToTsType() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestTsSafeName(t *testing.T) {
	if got := tsSafeName("interface"); got != "_interface" {
		t.Fatalf("tsSafeName(interface) = %q", got)
	}
	if got := tsSafeName("Instance"); got != "Instance" {
		t.Fatalf("tsSafeName(Instance) = %q", got)
	}
	// `constructor` must be escaped so an operation ID normalising to it is not
	// emitted as the class constructor.
	if got := tsSafeName("constructor"); got != "_constructor" {
		t.Fatalf("tsSafeName(constructor) = %q", got)
	}
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
			if got := schemaToTsType(c.schema); got != c.want {
				t.Fatalf("schemaToTsType() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSchemaToTsTypeComposition(t *testing.T) {
	oneOf := &openapi3.Schema{OneOf: openapi3.SchemaRefs{
		{Ref: "#/components/schemas/A"},
		{Ref: "#/components/schemas/B"},
	}}
	if got := schemaToTsType(oneOf); got != "A | B" {
		t.Fatalf("oneOf = %q, want %q", got, "A | B")
	}
	anyOf := &openapi3.Schema{AnyOf: openapi3.SchemaRefs{
		{Value: &openapi3.Schema{Type: strType("string")}},
		{Ref: "#/components/schemas/B"},
	}}
	if got := schemaToTsType(anyOf); got != "string | B" {
		t.Fatalf("anyOf = %q, want %q", got, "string | B")
	}
	allOf := &openapi3.Schema{AllOf: openapi3.SchemaRefs{
		{Ref: "#/components/schemas/A"},
		{Ref: "#/components/schemas/B"},
	}}
	if got := schemaToTsType(allOf); got != "A & B" {
		t.Fatalf("allOf = %q, want %q", got, "A & B")
	}

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
	if got := schemaToTsType(itemAllOfObject); got != "(A & B)[]" {
		t.Fatalf("array item allOf object = %q, want %q", got, "(A & B)[]")
	}
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
		if got := tf.qualifyModels("models", c.in); got != c.want {
			t.Fatalf("qualifyModels(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestQuoteTsControlChars(t *testing.T) {
	if got := quoteTs("a\nb\tc"); got != `"a\nb\tc"` {
		t.Fatalf("quoteTs(control) = %q", got)
	}
	if got := quoteTs(`a"b\c`); got != `"a\"b\\c"` {
		t.Fatalf("quoteTs(quotes) = %q", got)
	}
}

func TestTsDocEscapesTerminator(t *testing.T) {
	got := tsDoc("closes */ here", "")
	if strings.Contains(got, "*/ here") {
		t.Fatalf("tsDoc did not escape comment terminator: %q", got)
	}
}

func TestTsArrayOfUnion(t *testing.T) {
	if got := tsArrayOf("A | B"); got != "(A | B)[]" {
		t.Fatalf("tsArrayOf(union) = %q", got)
	}
	if got := tsArrayOf("Instance"); got != "Instance[]" {
		t.Fatalf("tsArrayOf(single) = %q", got)
	}
}

func TestReservedWordsSorted(t *testing.T) {
	for i := 1; i < len(tsReservedWords); i++ {
		if tsReservedWords[i-1] >= tsReservedWords[i] {
			t.Fatalf("tsReservedWords not sorted at %d: %q >= %q", i, tsReservedWords[i-1], tsReservedWords[i])
		}
	}
}
