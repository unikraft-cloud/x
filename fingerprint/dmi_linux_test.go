// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package fingerprint

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadDmi(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sys_vendor"), []byte("Caf\xe9 Systems\n"), 0o600))

	assert.Equal(t, "Caf� Systems", readDmi(dir, "sys_vendor"))
	assert.Empty(t, readDmi(dir, "product_name"))
}
