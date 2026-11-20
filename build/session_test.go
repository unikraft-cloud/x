// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"testing"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/stretchr/testify/require"
)

func TestServeSessionDuplicateService(t *testing.T) {
	sync := filesync.NewFSSyncProvider(filesync.StaticDirSource{})

	_, _, err := serveSession(t.Context(), nil, "", []session.Attachable{sync, sync})
	require.ErrorContains(t, err, "moby.filesync.v1.FileSync two times")
}
