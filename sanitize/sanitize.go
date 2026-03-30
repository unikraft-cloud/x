// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package sanitize

import (
	"regexp"
	"strings"
)

// secretPatterns contains compiled regexes for common secret patterns.
// Each pattern is designed to match sensitive data that should be redacted.
var secretPatterns = []*regexp.Regexp{
	// API keys and tokens (generic patterns)
	regexp.MustCompile(`(?i)(api[_-]?key|apikey|api[_-]?token|access[_-]?token|auth[_-]?token|bearer)\s*[:=]\s*["']?([a-zA-Z0-9_\-./+=]{16,})["']?`),
	regexp.MustCompile(`(?i)bearer\s+[a-zA-Z0-9_\-./+=]{20,}`),

	// JWT tokens
	regexp.MustCompile(`eyJ[a-zA-Z0-9_-]*\.eyJ[a-zA-Z0-9_-]*\.[a-zA-Z0-9_-]*`),

	// AWS credentials
	regexp.MustCompile(`(?i)(aws[_-]?access[_-]?key[_-]?id|aws[_-]?secret[_-]?access[_-]?key)\s*[:=]\s*["']?([A-Za-z0-9/+=]{16,})["']?`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),

	// Private keys
	regexp.MustCompile(`-----BEGIN\s+(RSA|EC|DSA|OPENSSH|PGP)?\s*PRIVATE KEY-----`),

	// Passwords in connection strings or assignments
	regexp.MustCompile(`(?i)(password|passwd|pwd|secret)\s*[:=]\s*["']?([^\s"']{4,})["']?`),

	// Connection strings with credentials
	regexp.MustCompile(`(?i)(mongodb|postgres|mysql|redis|amqp|smtp)://[^:]+:[^@]+@`),

	// GitHub/GitLab tokens
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9_]{36,}`),
	regexp.MustCompile(`glpat-[A-Za-z0-9_\-]{20,}`),

	// Generic hex secrets (32+ chars, likely hashes/keys)
	regexp.MustCompile(`(?i)(secret|token|key|hash)\s*[:=]\s*["']?([a-f0-9]{32,})["']?`),

	// PostHog API keys
	regexp.MustCompile(`phc_[A-Za-z0-9]{32,}`),

	// Base64 encoded data that looks like secrets (in key=value context)
	regexp.MustCompile(`(?i)(secret|token|key|credential)\s*[:=]\s*["']?([A-Za-z0-9+/]{40,}={0,2})["']?`),

	// Email addresses (can be PII)
	regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`),

	// IP addresses with ports (internal infrastructure)
	regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?):\d{1,5}\b`),
}

// SanitizeErrorMessage removes potentially sensitive information from error
// messages by redacting common secret patterns.
func SanitizeErrorMessage(msg string) string {
	// Take only the first line
	if idx := strings.Index(msg, "\n"); idx != -1 {
		msg = msg[:idx]
	}

	// Redact sensitive patterns
	for _, pattern := range secretPatterns {
		msg = pattern.ReplaceAllStringFunc(msg, func(match string) string {
			// For patterns with key=value structure, preserve the key
			if strings.ContainsAny(match, ":=") {
				parts := regexp.MustCompile(`[:=]`).Split(match, 2)
				if len(parts) == 2 {
					return parts[0] + "=[REDACTED]"
				}
			}
			return "[REDACTED]"
		})
	}

	// Truncate long messages
	if len(msg) > 200 {
		msg = msg[:200] + "..."
	}

	return msg
}
