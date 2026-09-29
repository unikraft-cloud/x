// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package openapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImportAlias(t *testing.T) {
	cases := []struct {
		importPath string
		want       string
	}{
		{"", ""},
		{"github.com/org/repo/api/common/v1", "commonv1"},
		{"github.com/org/repo/api/common/v12", "commonv12"},
		{"github.com/org/repo/api/common", "common"},
		{"github.com/org/repo/my-common", "mycommon"},
		{"github.com/org/repo/vfoo", "vfoo"},
		{"v1", "v1"},
	}

	for _, tc := range cases {
		t.Run(tc.importPath, func(t *testing.T) {
			require.Equal(t, tc.want, importAlias(tc.importPath))
		})
	}
}
