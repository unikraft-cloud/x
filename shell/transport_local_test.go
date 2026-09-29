// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/stdio"
)

func TestLocalTransportRunsCommands(t *testing.T) {
	root := newFixture(t)

	var out, errOut bytes.Buffer
	status, err := LocalTransport().Exec(t.Context(), Command{
		Args:    []string{"sh", "-c", "pwd; echo $GREETING; echo oops >&2; cat; exit 4"},
		Dir:     root,
		Env:     []string{"GREETING=hello"},
		Streams: stdio.Stdio{Stdin: strings.NewReader("piped\n"), Stdout: &out, Stderr: &errOut},
	})

	require.NoError(t, err)
	assert.Equal(t, 4, status)
	assert.Equal(t, root+"\nhello\npiped\n", out.String())
	assert.Equal(t, "oops\n", errOut.String())
}
