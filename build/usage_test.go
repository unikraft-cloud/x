// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsageOf(t *testing.T) {
	usage, err := usageOf([]byte(`{"buildDefinition": {"internalParameters": {"buildConfig": {
		"llbDefinition": [
			{"id": "step0", "resourceUsage": {"samples": [{"pidsStat": {"current": 3}}]}},
			{"id": "step1"}
		],
		"digestMapping": {"sha256:a1": "step0", "sha256:b2": "step1"}
	}}}}`))
	require.NoError(t, err)
	require.Len(t, usage, 1)
	assert.Equal(t, uint64(3), *usage["sha256:a1"].Samples[0].PIDsStat.Current)
}
