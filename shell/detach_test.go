// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/stdio"
)

func TestDetachPolicy(t *testing.T) {
	t.Run("a-statement-is-waited-for", func(t *testing.T) {
		transport := newHaltingTransport()
		s, _ := newHaltingSession(t, transport)

		interruptOnce(transport, s.interrupts)
		runShellLine(t, s, blockingCommand)

		assert.False(t, transport.lastDetached(t))
	})

	t.Run("a-probe-costs-nothing", func(t *testing.T) {
		transport := newHaltingTransport()

		_, _ = ExecTransport(transport.Exec).Environ(t.Context())

		assert.True(t, transport.lastDetached(t))
	})

	t.Run("a-file-test-in-a-statement-is-not-a-probe", func(t *testing.T) {
		transport := newHaltingTransport()

		_, err := ExecTransport(transport.Exec).Stat(t.Context(), "/", "/tmp", true)

		require.Error(t, err, "the halting transport answers nothing useful")
		assert.False(t, transport.lastDetached(t),
			"the statement is waiting for it, and its ^C ends it")
	})

	t.Run("a-single-command-line-is-waited-for-too", func(t *testing.T) {
		transport := newHaltingTransport()
		_, err := Run(t.Context(), Config{
			Instance:  "fake",
			Dir:       "/",
			Command:   "once",
			Transport: ExecTransport(transport.Exec),
		}, stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &captured{}, Stderr: &captured{}})
		require.NoError(t, err)

		assert.False(t, transport.lastDetached(t))
	})
}
