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

func TestGetTypePackage(t *testing.T) {
	// Covers a plain namespaced type, an x-package-tagged type, and an unnamespaced type.
	schemas := openapi3.Schemas{
		"UnikraftCloud.Common.ResponseStatus": {Value: &openapi3.Schema{Type: strType("string"), Enum: []any{"success"}}},
		"Local.Tagged":                        {Value: &openapi3.Schema{Type: strType("object"), Extensions: map[string]any{"x-package": "override"}}},
		"Bare":                                {Value: &openapi3.Schema{Type: strType("object")}},
	}
	models := []Model{
		{SchemaName: "UnikraftCloud.Common.ResponseStatus", Schema: schemas["UnikraftCloud.Common.ResponseStatus"].Value, Namespace: "Common"},
		{SchemaName: "Local.Tagged", Schema: schemas["Local.Tagged"].Value, Package: "override", Namespace: "Local"},
		{SchemaName: "Bare", Schema: schemas["Bare"].Value},
	}

	parser := &Parser{doc: &openapi3.T{Components: &openapi3.Components{Schemas: schemas}}}
	parser.Flatten(models, "strip")
	tf := &templateFuncs{parser: parser}

	require.Equal(t, "common", tf.getTypePackage("#/components/schemas/ResponseStatus"))
	require.Equal(t, "override", tf.getTypePackage("#/components/schemas/Tagged"))
	require.Empty(t, tf.getTypePackage("#/components/schemas/Bare"))
}

func TestGetTypePackageNestedNamespace(t *testing.T) {
	// A nested namespace resolves to its innermost segment lowercased.
	schemas := openapi3.Schemas{
		"UnikraftCloud.Common.Console.NameOrUUID": {Value: &openapi3.Schema{Type: strType("object")}},
	}
	models := []Model{
		{SchemaName: "UnikraftCloud.Common.Console.NameOrUUID", Schema: schemas["UnikraftCloud.Common.Console.NameOrUUID"].Value, Namespace: "Console"},
	}

	parser := &Parser{doc: &openapi3.T{Components: &openapi3.Components{Schemas: schemas}}}
	parser.Flatten(models, "strip")
	tf := &templateFuncs{parser: parser}

	require.Equal(t, "console", tf.getTypePackage("#/components/schemas/NameOrUUID"))
}

func TestGetTypePackageGeneratedHereIsLocal(t *testing.T) {
	// An imported namespace generated alongside its users needs no qualifier.
	schemas := openapi3.Schemas{
		"UnikraftCloud.Common.ResponseStatus": {Value: &openapi3.Schema{Type: strType("string"), Enum: []any{"success"}}},
	}
	models := []Model{
		{SchemaName: "UnikraftCloud.Common.ResponseStatus", Schema: schemas["UnikraftCloud.Common.ResponseStatus"].Value, Namespace: "Common"},
	}

	parser := &Parser{doc: &openapi3.T{Components: &openapi3.Components{Schemas: schemas}}}
	parser.Flatten(models, "strip")
	tf := &templateFuncs{parser: parser, models: &models}

	require.Empty(t, tf.getTypePackage("#/components/schemas/ResponseStatus"))

	// Filtered out of this run, the same type resolves to its own package.
	none := []Model{}
	tf = &templateFuncs{parser: parser, models: &none}
	require.Equal(t, "common", tf.getTypePackage("#/components/schemas/ResponseStatus"))
}
