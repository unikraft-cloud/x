// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package fingerprint_test

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/fingerprint"
)

func TestNew(t *testing.T) {
	fp, err := fingerprint.New()
	require.NoError(t, err)
	require.NotNil(t, fp)

	assert.Equal(t, runtime.GOARCH, fp.Goarch, "reports the build's architecture")
	assert.Equal(t, runtime.GOOS, fp.Goos, "reports the build's operating system")
	assert.NotEmpty(t, fp.Hostname, "always names the machine")
	assert.NotEmpty(t, fp.Os, "always names the operating system")

	require.NotNil(t, fp.GoVersion)
	assert.Equal(t, runtime.Version(), *fp.GoVersion)
}
