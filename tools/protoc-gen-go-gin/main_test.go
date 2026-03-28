// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import "testing"

func TestFlattenGoName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Request_Body", "RequestBody"},
		{"Request_Item", "RequestItem"},
		{"UpdateInstanceByUUIDRequest_Body", "UpdateInstanceByUUIDRequestBody"},
		{"A_B_C", "ABC"},
		// Top-level names must pass through unchanged.
		{"CreateInstanceRequest", "CreateInstanceRequest"},
		{"GetInstancesResponse", "GetInstancesResponse"},
		{"Body", "Body"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := flattenGoName(tt.input)
			if got != tt.want {
				t.Errorf("flattenGoName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
