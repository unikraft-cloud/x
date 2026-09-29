// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pluginExit is an exit status a transport of its own reports, as the sandbox
// plugin's does.
type pluginExit struct{ code int }

func (e *pluginExit) Error() string { return "command exited" }
func (e *pluginExit) ExitCode() int { return e.code }

func TestExitStatus(t *testing.T) {
	t.Run("a command that ran", func(t *testing.T) {
		status, err := ExitStatus(nil)

		require.NoError(t, err)
		assert.Equal(t, 0, status)
	})

	t.Run("a command that exited", func(t *testing.T) {
		status, err := ExitStatus(exec.Command("sh", "-c", "exit 3").Run())

		require.NoError(t, err, "an exit is a status, not a failure")
		assert.Equal(t, 3, status)
	})

	t.Run("a command a signal ended", func(t *testing.T) {
		status, err := ExitStatus(exec.Command("sh", "-c", "kill -TERM $$").Run())

		require.NoError(t, err)
		assert.Equal(t, -int(syscall.SIGTERM), status, "a signal is its number negated")
	})

	t.Run("a transport that reports its own status", func(t *testing.T) {
		status, err := ExitStatus(&pluginExit{code: 7})

		require.NoError(t, err)
		assert.Equal(t, 7, status)
	})

	t.Run("a transport that names no status", func(t *testing.T) {
		status, err := ExitStatus(&pluginExit{code: -1})

		require.NoError(t, err)
		assert.Equal(t, StatusInterrupted, status, "-1 is a signal it did not name")
	})

	t.Run("a transport that failed", func(t *testing.T) {
		failed := errors.New("connection reset by peer")

		status, err := ExitStatus(failed)

		require.ErrorIs(t, err, failed)
		assert.Equal(t, 0, status)
	})
}
