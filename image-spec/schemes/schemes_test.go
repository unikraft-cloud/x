// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package schemes_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/image-spec/schemes"
)

func TestParse(t *testing.T) {
	t.Parallel()

	for _, s := range []schemes.Scheme{schemes.OCI, schemes.OCILayout, schemes.OCIArchive, schemes.HTTPOCI, schemes.HTTPSOCI} {
		got, err := schemes.Parse(string(s))
		require.NoError(t, err)
		assert.Equal(t, s, got)
		assert.Equal(t, s == schemes.HTTPOCI || s == schemes.HTTPSOCI, got.IsHTTP())
	}

	_, err := schemes.Parse("docker")
	assert.ErrorIs(t, err, schemes.ErrUnsupported)
}
