// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2025, The buildx authors.
// Licensed under the Apache License, Version 2.0 (the "License").
// You may not use this file except in compliance with the License.

package buildflags

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateSSHWithoutAnAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("buildkit falls back to the named pipe of the OpenSSH agent")
	}
	t.Setenv("SSH_AUTH_SOCK", "")

	_, err := CreateSSH([]*SSH{{ID: "default"}})
	require.EqualError(t, err, "ssh default: no ssh agent: set SSH_AUTH_SOCK, or give a socket or keys as default=<path>")
}
