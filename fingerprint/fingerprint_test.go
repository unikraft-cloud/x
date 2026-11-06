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

func TestNewWithoutOptional(t *testing.T) {
	fp, err := fingerprint.New(
		fingerprint.WithMachineId(false),
		fingerprint.WithCpu(false),
		fingerprint.WithMemory(false),
		fingerprint.WithKernel(false),
	)
	require.NoError(t, err)
	require.NotNil(t, fp)

	assert.Empty(t, fp.MachineId)
	assert.Nil(t, fp.CpuCores)
	assert.Nil(t, fp.CpusThreads)
	assert.Nil(t, fp.CpuVendorId)
	assert.Nil(t, fp.CpuFamily)
	assert.Nil(t, fp.CpuModel)
	assert.Nil(t, fp.CpuModelName)
	assert.Nil(t, fp.CpuMhz)
	assert.Nil(t, fp.CpuCacheSize)
	assert.Nil(t, fp.CpuFlags)
	assert.Nil(t, fp.CpuMicrocode)
	assert.Nil(t, fp.MemTotal)
	assert.Nil(t, fp.KernelFeatures)
	assert.Nil(t, fp.KernelRelease)
	assert.Nil(t, fp.KernelVersion)

	assert.Equal(t, runtime.GOARCH, fp.Goarch, "still reports the build's architecture")
	assert.Equal(t, runtime.GOOS, fp.Goos, "still reports the build's operating system")
	assert.NotEmpty(t, fp.Hostname, "still names the machine")
	assert.NotEmpty(t, fp.Os, "still names the operating system")

	require.NotNil(t, fp.GoVersion)
	assert.Equal(t, runtime.Version(), *fp.GoVersion)
}
