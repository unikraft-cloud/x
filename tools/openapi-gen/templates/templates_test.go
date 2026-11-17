// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package templates

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/tools/openapi-gen/generator"
)

func TestPacksMatchExamples(t *testing.T) {
	for name, pack := range map[string]*generator.Pack{
		"client": GoClient,
		"server": GoServer,
	} {
		t.Run(name, func(t *testing.T) {
			g, err := generator.NewGenerator("../examples/api.yaml", map[string]string{"package": name}, pack)
			require.NoError(t, err)

			files, err := g.Render()
			require.NoError(t, err)

			want, err := filepath.Glob(filepath.Join("../examples", name, "*.gen.go"))
			require.NoError(t, err)
			require.Len(t, files, len(want))

			for _, f := range files {
				expected, err := os.ReadFile(filepath.Join("../examples", name, f.Name))
				require.NoError(t, err)
				assert.Equal(t, string(expected), string(f.Data), f.Name)
			}
		})
	}
}
