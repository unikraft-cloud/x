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
        statusByKey:
          type: object
          additionalProperties:
            $ref: '#/components/schemas/Org.Common.Status'
    Org.Common.Status:
      type: string
`

const modelsTemplate = `--- models.txt
{{- range .Models}}
{{.SchemaName}}
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

// An imported namespace is not generated here, so only local models remain.
func TestRunNamespaceImportPathDropsImportedModels(t *testing.T) {
	got := writeRun(t, namespacedSpec, modelsTemplate, Options{
		NamespaceImportPath: map[string]string{"Org.Common": "example.com/api/common/v1"},
		Flatten:             "strip",
	})

	require.Contains(t, got, "Machine")
	require.NotContains(t, got, "Status")
}

// Without the mapping the same schema is generated alongside its users.
func TestRunWithoutNamespaceImportPathKeepsModels(t *testing.T) {
	got := writeRun(t, namespacedSpec, modelsTemplate, Options{Flatten: "strip"})

	require.Contains(t, got, "Machine")
	require.Contains(t, got, "Status")
}

// A reference to an imported type resolves to the package at its import path.
func TestRunNamespaceImportPathQualifiesReferences(t *testing.T) {
	tmpl := `--- models.txt
{{- range $model := .Models}}
{{- range $name := propertyNamesOrdered $model.SchemaName $model.Schema}}
{{- $prop := getProperty $model.Schema $name}}
{{$name}} {{getTypePackage $prop}} {{getTypeImportPath $prop}} {{schemaToGoType $prop}}
{{- end}}
{{- end}}
`
	got := writeRun(t, namespacedSpec, tmpl, Options{
		NamespaceImportPath: map[string]string{"Org.Common": "example.com/api/common/v1"},
		Flatten:             "strip",
	})

	require.Contains(t, got, "status commonv1 example.com/api/common/v1 Status")
	require.Contains(t, got, "statusByKey commonv1 example.com/api/common/v1 map[string]Status")
}

// A local component that only refers to an imported one stays local.
func TestRunNamespaceImportPathKeepsLocalAlias(t *testing.T) {
	spec := `openapi: 3.0.0
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
          $ref: '#/components/schemas/MachineStatus'
        common:
          $ref: '#/components/schemas/Org.Common.Status'
    MachineStatus:
      $ref: '#/components/schemas/Org.Common.Status'
    Org.Common.Status:
      type: string
`
	tmpl := `--- models.txt
{{- range $model := .Models}}
{{- range $name := propertyNamesOrdered $model.SchemaName $model.Schema}}
{{$name}} [{{getTypePackage (getProperty $model.Schema $name)}}]
{{- end}}
{{- end}}
`
	got := writeRun(t, spec, tmpl, Options{
		NamespaceImportPath: map[string]string{"Org.Common": "example.com/api/common/v1"},
		Flatten:             "strip",
	})

	require.Contains(t, got, "status []")
	require.Contains(t, got, "common [commonv1]")
}

func TestRunNamespaceImportPathErrors(t *testing.T) {
	t.Parallel()

	spec := `openapi: 3.0.0
info:
  title: Test
  version: "1.0"
paths: {}
components:
  schemas:
    Org.Common.Status:
      type: string
    Org.Common.Label.Label:
      type: string
`
	tests := []struct {
		name    string
		imports map[string]string
		flatten string
		extra   string
		wantErr string
	}{
		{
			name:    "unknown namespace",
			imports: map[string]string{"Org.Comon": "example.com/repo/api/common/v1"},
			wantErr: `namespace "Org.Comon" has no schemas`,
		},
		{
			name:    "flatten clash with imported name",
			imports: map[string]string{"Org.Common": "example.com/repo/api/common/v1"},
			flatten: "strip",
			extra: `    Status:
      type: integer
`,
			wantErr: `schemas "Org.Common.Status" and "Status" both flatten to "Status"`,
		},
		{
			name: "same package name",
			imports: map[string]string{
				"Org.Common":       "example.com/a/common/v1",
				"Org.Common.Label": "example.com/b/common/v1",
			},
			wantErr: `both use package name "commonv1"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			specPath := filepath.Join(dir, "spec.yaml")
			require.NoError(t, os.WriteFile(specPath, []byte(spec+tc.extra), 0o600))
			tmplDir := filepath.Join(dir, "templates")
			require.NoError(t, os.Mkdir(tmplDir, 0o750))
			require.NoError(t, os.WriteFile(filepath.Join(tmplDir, "models.tmpl"), []byte(modelsTemplate), 0o600))

			err := Run(Options{
				Input:               specPath,
				Templates:           tmplDir,
				Output:              filepath.Join(dir, "out"),
				NamespaceImportPath: tc.imports,
				Flatten:             tc.flatten,
			})
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestRunModelsOnlyRejectsTag(t *testing.T) {
	t.Parallel()

	err := Run(Options{Input: "unused.yaml", ModelsOnly: true, Tag: []string{"Instances"}})
	require.ErrorContains(t, err, "cannot be combined")
}
