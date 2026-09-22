// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNamespacePackages(t *testing.T) {
	cases := []struct {
		name  string
		value []string
		want  map[string]string
	}{
		{"none", nil, nil},
		{"explicit package", []string{"Org.Common=shared"}, map[string]string{"Org.Common": "shared"}},
		{"defaulted package", []string{"Org.Common"}, map[string]string{"Org.Common": "common"}},
		{"empty package", []string{"Org.Common="}, map[string]string{"Org.Common": "common"}},
		{"single segment", []string{"Common"}, map[string]string{"Common": "common"}},
		{
			"repeated",
			[]string{"Org.Common", "Org.Billing=billing"},
			map[string]string{"Org.Common": "common", "Org.Billing": "billing"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, namespacePackages(tc.value))
		})
	}
}
