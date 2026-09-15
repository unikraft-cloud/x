// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package generator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"unikraft.com/x/tools/openapi-gen/internal/openapi"
)

func TestFilterByPackageFallsBackToNamespace(t *testing.T) {
	g := &Generator{
		models: []openapi.Model{
			{SchemaName: "Common.ResponseStatus", Namespace: "Common"},
			{SchemaName: "Local.Tagged", Package: "override", Namespace: "Local"},
			{SchemaName: "Controlplane.Node", Namespace: "Controlplane"},
		},
	}

	g.FilterByPackage("common")

	require.Len(t, g.models, 1)
	require.Equal(t, "Common.ResponseStatus", g.models[0].SchemaName)
}

// TestFilterByNamespaceKeepsUnnamespacedModels covers a TypeSpec OpenAPI3
// document, where a service's own schemas are emitted with no namespace
// prefix at all and only foreign, imported types carry one: an unnamespaced
// model must survive a --namespace filter, not be dropped as unmatched.
func TestFilterByNamespaceKeepsUnnamespacedModels(t *testing.T) {
	g := &Generator{
		models: []openapi.Model{
			{SchemaName: "Node", Namespace: ""},
			{SchemaName: "Common.ResponseStatus", Namespace: "Common"},
		},
	}

	g.FilterByNamespace([]string{"Controlplane"}, false)

	require.Len(t, g.models, 1)
	require.Equal(t, "Node", g.models[0].SchemaName)
}

// TestFilterByNamespaceStrictDropsUnnamespacedModels covers the run that
// generates a shared package for one imported namespace: there the document's
// own schemas are what must go, so the package holds the namespace alone.
func TestFilterByNamespaceStrictDropsUnnamespacedModels(t *testing.T) {
	g := &Generator{
		models: []openapi.Model{
			{SchemaName: "Node", Namespace: ""},
			{SchemaName: "Common.ResponseStatus", Namespace: "Common"},
			{SchemaName: "Console.NameOrUUID", Namespace: "Console"},
		},
	}

	g.FilterByNamespace([]string{"Common"}, true)

	require.Len(t, g.models, 1)
	require.Equal(t, "Common.ResponseStatus", g.models[0].SchemaName)
}

func TestExcludeByNamespaceDropsOnlyNamedNamespaces(t *testing.T) {
	g := &Generator{
		models: []openapi.Model{
			{SchemaName: "Node", Namespace: ""},
			{SchemaName: "Common.ResponseStatus", Namespace: "Common"},
			{SchemaName: "Console.NameOrUUID", Namespace: "Console"},
		},
	}

	g.ExcludeByNamespace([]string{"Common"})

	require.Len(t, g.models, 2)
	require.Equal(t, "Node", g.models[0].SchemaName)
	require.Equal(t, "Console.NameOrUUID", g.models[1].SchemaName)
}

func TestExcludeByNamespaceWithoutNamespacesKeepsEverything(t *testing.T) {
	g := &Generator{
		models: []openapi.Model{{SchemaName: "Node", Namespace: ""}},
	}

	g.ExcludeByNamespace(nil)

	require.Len(t, g.models, 1)
}

func TestFilterByPackagePrefersExplicitTag(t *testing.T) {
	g := &Generator{
		models: []openapi.Model{
			{SchemaName: "Common.Overridden", Package: "other", Namespace: "Common"},
		},
	}

	g.FilterByPackage("common")

	require.Empty(t, g.models)
}

// TestRunNamespaceOnlyDoesNotSelfQualify covers a --namespace-only
// (TypeSpec) run: a type local to the namespace being generated must render
// bare, not qualified with (and self-imported from) its own package.
func TestRunNamespaceOnlyDoesNotSelfQualify(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "api.yaml")
	require.NoError(t, os.WriteFile(spec, []byte(`
openapi: 3.0.3
info:
  title: test
  version: "1.0"
paths: {}
components:
  schemas:
    Controlplane.Node:
      type: object
      properties:
        states:
          type: array
          items:
            $ref: '#/components/schemas/Controlplane.NodeState'
      required:
        - states
    Controlplane.NodeState:
      type: object
      properties:
        status:
          type: string
      required:
        - status
`), 0o644))

	out := filepath.Join(dir, "out")
	require.NoError(t, Run(Options{
		Input:     spec,
		Output:    out,
		Var:       map[string]string{"package": "controlplanev1"},
		Templates: "../templates/go-server",
		Namespace: []string{"Controlplane"},
		Flatten:   "strip",
	}))

	generated, err := os.ReadFile(filepath.Join(out, "struct.gen.go"))
	require.NoError(t, err)

	require.Contains(t, string(generated), "States []NodeState")
	require.NotContains(t, string(generated), "controlplanev1.")
	require.NotContains(t, string(generated), `"api/controlplane/v1"`)
}

// TestRunArrayQueryParamCrossPackageQualification covers a gin handler with an
// array-typed query parameter whose item type is filtered out of this run and
// so belongs to a foreign package: the package qualifier must land on the item
// type ("[]pkgv1.Type"), not before the slice brackets ("pkgv1.[]Type"), which
// gofmt rejects.
func TestRunArrayQueryParamCrossPackageQualification(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "api.yaml")
	require.NoError(t, os.WriteFile(spec, []byte(`
openapi: 3.0.3
info:
  title: test
  version: "1.0"
paths:
  /nodes:
    get:
      operationId: ListNodes
      tags:
        - NodeService
      parameters:
        - name: states
          in: query
          required: false
          schema:
            type: array
            items:
              $ref: '#/components/schemas/Common.CommonState'
      responses:
        default:
          description: ok
          content:
            application/json:
              schema:
                type: object
components:
  schemas:
    Common.CommonState:
      type: string
      enum:
        - active
        - inactive
`), 0o644))

	out := filepath.Join(dir, "out")
	require.NoError(t, Run(Options{
		Input:  spec,
		Output: out,
		Var: map[string]string{
			"package":         "controlplanev1",
			"current_package": "controlplane",
			"base_package":    "github.com/unikraft-cloud/console/api",
		},
		Templates: "../templates/go-server",
		Namespace: []string{"Controlplane"},
		Flatten:   "strip",
	}))

	generated, err := os.ReadFile(filepath.Join(out, "node_service_gin.gen.go"))
	require.NoError(t, err)

	require.Contains(t, string(generated), "[]commonv1.CommonState")
	require.NotContains(t, string(generated), "commonv1.[]CommonState")
}
