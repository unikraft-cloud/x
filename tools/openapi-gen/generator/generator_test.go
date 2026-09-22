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
)

const namespacedSpec = `openapi: 3.0.0
info:
  title: Test
  version: "1.0"
paths: {}
components:
  schemas:
    Machine:
      type: object
      properties:
        status:
          $ref: '#/components/schemas/Org.Common.Status'
    Org.Common.Status:
      type: string
`

const modelsTemplate = `--- models.txt
{{- range .Models}}
{{.SchemaName}} {{$.Var "x-package" ""}}
{{- end}}
`

// writeRun writes spec and template to temp dirs and runs opts against them.
func writeRun(t *testing.T, spec, tmpl string, opts Options) string {
	t.Helper()

	dir := t.TempDir()
	specPath := filepath.Join(dir, "spec.yaml")
	require.NoError(t, os.WriteFile(specPath, []byte(spec), 0o600))

	tmplDir := filepath.Join(dir, "templates")
	require.NoError(t, os.Mkdir(tmplDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(tmplDir, "models.tmpl"), []byte(tmpl), 0o600))

	out := filepath.Join(dir, "out")
	opts.Input = specPath
	opts.Templates = tmplDir
	opts.Output = out
	require.NoError(t, Run(opts))

	got, err := os.ReadFile(filepath.Join(out, "models.txt"))
	require.NoError(t, err)
	return string(got)
}

// A mapped namespace is not generated here, so only local models remain.
func TestRunNamespacePackageDropsMappedModels(t *testing.T) {
	got := writeRun(t, namespacedSpec, modelsTemplate, Options{
		NamespacePackage: map[string]string{"Org.Common": "common"},
		Flatten:          "strip",
	})

	require.Contains(t, got, "Machine")
	require.NotContains(t, got, "Status")
}

// Without the mapping the same schema is generated alongside its users.
func TestRunWithoutNamespacePackageKeepsModels(t *testing.T) {
	got := writeRun(t, namespacedSpec, modelsTemplate, Options{Flatten: "strip"})

	require.Contains(t, got, "Machine")
	require.Contains(t, got, "Status")
}

// A reference to a mapped type resolves to the package the mapping names.
func TestRunNamespacePackageQualifiesReferences(t *testing.T) {
	tmpl := `--- models.txt
{{- range $model := .Models}}
{{- range $name := propertyNamesOrdered $model.SchemaName $model.Schema}}
{{- $prop := getProperty $model.Schema $name}}
{{$name}} {{getTypePackage $prop}} {{schemaToGoType $prop}}
{{- end}}
{{- end}}
`
	got := writeRun(t, namespacedSpec, tmpl, Options{
		NamespacePackage: map[string]string{"Org.Common": "common"},
		Flatten:          "strip",
	})

	require.Contains(t, got, "status common Status")
}
