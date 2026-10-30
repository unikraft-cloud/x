// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package integration holds the helpers of the tests that run against
// Unikraft Cloud, built with the integration tag.
package integration

import (
	"os"
	"strconv"
	"testing"
)

// Image is the base image of every integration test. It is multi-arch and
// has absolute symlinks in its own layers.
const Image = "docker.io/library/debian:bookworm-slim"

// Token gives the Unikraft Cloud token in UKC_TOKEN, and skips the test
// without one.
func Token(t testing.TB) string {
	t.Helper()
	token := os.Getenv("UKC_TOKEN")
	if token == "" {
		t.Skip("skipping integration test (UKC_TOKEN not set)")
	}
	return token
}

// Insecure reports whether UKC_INSECURE is true, as the CI config of the cli
// sets for metros with self-signed certificates.
func Insecure() bool {
	insecure, _ := strconv.ParseBool(os.Getenv("UKC_INSECURE"))
	return insecure
}
