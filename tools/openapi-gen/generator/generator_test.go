// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package generator

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSpec = `openapi: 3.0.3
info:
  title: test
  version: "1"
paths: {}
components:
  schemas:
    Widget:
      type: object
      properties:
        name:
          type: string
`

func writeSpec(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "api.yaml")
	require.NoError(t, os.WriteFile(p, []byte(testSpec), 0o644))
	return p
}

func TestNewGenerator(t *testing.T) {
	fsys := fstest.MapFS{
		"models.txt.tmpl":   {Data: []byte(`{{ range .Models }}{{ template "line" . }}{{ end }}`)},
		"lib/helpers.tmpl":  {Data: []byte(`{{ define "line" }}{{ .SchemaName }}{{ end }}`)},
		"README.md":         {Data: []byte("not a template")},
		"nested/other.tmpl": {Data: []byte(`{{ .Var "package" "" }}`)},
	}

	g, err := NewGenerator(writeSpec(t), map[string]string{"package": "pkg"}, &Pack{FS: fsys})
	require.NoError(t, err)

	files, err := g.Render()
	require.NoError(t, err)

	got := map[string]string{}
	for _, f := range files {
		got[f.Name] = string(f.Data)
	}
	assert.Equal(t, map[string]string{
		"models.gen.txt": "Widget",
		"other.gen":      "pkg",
	}, got)
}
