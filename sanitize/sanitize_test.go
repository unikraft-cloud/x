// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package sanitize

import (
	"strings"
	"testing"
)

func TestSanitizeErrorMessage(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string // strings that should be in output
		excludes []string // strings that should NOT be in output
	}{
		{
			name:     "plain error message",
			input:    "connection refused",
			contains: []string{"connection refused"},
		},
		{
			name:     "multiline takes first line only",
			input:    "error on line 1\nerror on line 2",
			contains: []string{"error on line 1"},
			excludes: []string{"line 2"},
		},
		{
			name:     "truncates long messages",
			input:    strings.Repeat("a", 300),
			contains: []string{"..."},
		},
		{
			name:     "redacts API key assignment",
			input:    "failed with api_key=sk_live_abc123def456ghi789",
			contains: []string{"api_key=[REDACTED]"},
			excludes: []string{"sk_live_abc123def456ghi789"},
		},
		{
			name:     "redacts bearer token",
			input:    "auth failed: bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
			contains: []string{"[REDACTED]"},
			excludes: []string{"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"},
		},
		{
			name:     "redacts JWT token",
			input:    "token expired: eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
			contains: []string{"[REDACTED]"},
			excludes: []string{"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"},
		},
		{
			name:     "redacts AWS access key",
			input:    "AWS error: AKIAIOSFODNN7EXAMPLE not authorized",
			contains: []string{"[REDACTED]"},
			excludes: []string{"AKIAIOSFODNN7EXAMPLE"},
		},
		{
			name:     "redacts password in connection string",
			input:    "connection failed: postgres://user:secretpassword123@localhost:5432/db",
			contains: []string{"[REDACTED]"},
			excludes: []string{"secretpassword123"},
		},
		{
			name:     "redacts password assignment",
			input:    "login failed password=mysecretpass123",
			contains: []string{"password=[REDACTED]"},
			excludes: []string{"mysecretpass123"},
		},
		{
			name:     "redacts GitHub token",
			input:    "auth error with token ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
			contains: []string{"[REDACTED]"},
			excludes: []string{"ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},
		},
		{
			name:     "redacts PostHog API key",
			input:    "posthog error with key phc_abcdefghijklmnopqrstuvwxyz123456",
			contains: []string{"[REDACTED]"},
			excludes: []string{"phc_abcdefghijklmnopqrstuvwxyz123456"},
		},
		{
			name:     "redacts email addresses",
			input:    "user not found: john.doe@example.com",
			contains: []string{"[REDACTED]"},
			excludes: []string{"john.doe@example.com"},
		},
		{
			name:     "redacts IP with port",
			input:    "connection to 192.168.1.100:8080 failed",
			contains: []string{"[REDACTED]"},
			excludes: []string{"192.168.1.100:8080"},
		},
		{
			name:     "redacts secret hex string",
			input:    "invalid secret=a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
			contains: []string{"secret=[REDACTED]"},
			excludes: []string{"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"},
		},
		{
			name:     "redacts private key header",
			input:    "error parsing -----BEGIN RSA PRIVATE KEY----- block",
			contains: []string{"[REDACTED]"},
			excludes: []string{"PRIVATE KEY"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeErrorMessage(tt.input)

			for _, want := range tt.contains {
				if !strings.Contains(result, want) {
					t.Errorf("sanitizeErrorMessage() = %q, should contain %q", result, want)
				}
			}

			for _, exclude := range tt.excludes {
				if strings.Contains(result, exclude) {
					t.Errorf("sanitizeErrorMessage() = %q, should NOT contain %q", result, exclude)
				}
			}
		})
	}
}
