// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package kraftfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseDirectoryPrefersTheFirstDefaultFileName(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kraft.yaml"), []byte("spec: v0.7\nname: a\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Kraftfile"), []byte("spec: v0.7\nname: b\n"), 0o644))

	doc, err := ParseDirectory(dir)
	require.NoError(t, err)
	require.Equal(t, "a", doc.Name)
}
