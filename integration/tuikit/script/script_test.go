// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package script_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/integration/tuikit"
	"unikraft.com/x/integration/tuikit/script"
)

// counter counts the presses of "+".
type counter int

func (c counter) Init() tea.Cmd { return nil }

func (c counter) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "+" {
		c++
	}
	return c, nil
}

func (c counter) View() tea.View {
	return tea.NewView(fmt.Sprintf("count: %d", int(c)))
}

func TestExecutor(t *testing.T) {
	t.Parallel()

	t.Run("a script prints its snapshots", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		exec := &script.Executor{Runner: tuikit.Start(t, counter(0)), Out: &out, WaitTimeout: 5 * time.Second}

		err := exec.Run(t.Context(), strings.NewReader(`
# nothing pressed yet
wait contains("count: 0")
snapshot

key +
key +   # twice
wait contains("count: 2") and not contains("count: 0")
snapshot
`))
		require.NoError(t, err)
		assert.Equal(t, "count: 0\ncount: 2\n", out.String())
	})

	t.Run("a wait that never passes fails at its line", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		exec := &script.Executor{Runner: tuikit.Start(t, counter(0)), Out: &out, WaitTimeout: 50 * time.Millisecond}

		err := exec.Run(t.Context(), strings.NewReader("key +\nwait contains(\"count: 2\")\nsnapshot\n"))
		require.ErrorContains(t, err, "line 2")
		assert.Empty(t, out.String(), "the snapshot after the failure does not run")
	})

	t.Run("a line that does not parse fails at its line", func(t *testing.T) {
		t.Parallel()
		exec := &script.Executor{Runner: tuikit.Start(t, counter(0)), Out: &bytes.Buffer{}}

		err := exec.Run(t.Context(), strings.NewReader("snapshot\nclick here\n"))
		require.ErrorContains(t, err, "line 2")
	})

	t.Run("with JSON, each line is an event", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		exec := &script.Executor{Runner: tuikit.Start(t, counter(0)), Out: &out, JSON: true, WaitTimeout: 50 * time.Millisecond}

		err := exec.Run(t.Context(), strings.NewReader("key +\nwait contains(\"count: 1\")\n# a comment\nsnapshot\nwait contains(\"count: 9\")\n"))
		require.ErrorContains(t, err, "line 5")

		type event struct {
			Seq      int    `json:"seq"`
			Line     int    `json:"line"`
			Cmd      string `json:"cmd"`
			Kind     string `json:"kind"`
			Error    string `json:"error"`
			Snapshot string `json:"snapshot"`
		}
		var events []event
		dec := json.NewDecoder(&out)
		for dec.More() {
			var ev event
			require.NoError(t, dec.Decode(&ev))
			events = append(events, ev)
		}

		require.Len(t, events, 4)
		assert.Equal(t, event{Seq: 1, Line: 1, Cmd: "key +", Kind: "key"}, events[0])
		assert.Equal(t, event{Seq: 2, Line: 2, Cmd: `wait contains("count: 1")`, Kind: "wait"}, events[1])
		assert.Equal(t, event{Seq: 3, Line: 4, Cmd: "snapshot", Kind: "snapshot", Snapshot: "count: 1"}, events[2])
		assert.Equal(t, 5, events[3].Line)
		assert.NotEmpty(t, events[3].Error)
		assert.Equal(t, "count: 1", events[3].Snapshot, "a failure carries the last snapshot")
	})

	t.Run("a wait has a timeout without being given one", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		exec := &script.Executor{Runner: tuikit.Start(t, counter(0)), Out: &out}

		err := exec.Run(t.Context(), strings.NewReader("key +\nwait contains(\"count: 1\")\nsnapshot\n"))
		require.NoError(t, err)
		assert.Equal(t, "count: 1\n", out.String())
	})

	t.Run("a script stops when its context is done", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		exec := &script.Executor{Runner: tuikit.Start(t, counter(0)), Out: &out}

		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := exec.Run(ctx, strings.NewReader("sleep 1m\nkey +\n"))
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.ErrorContains(t, err, "line 1")

		ctx, cancel = context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		err = exec.Run(ctx, strings.NewReader("wait contains(\"never\")\nkey +\n"))
		require.ErrorContains(t, err, "line 1")
		assert.Less(t, time.Since(start), 4*time.Second)

		require.NoError(t, exec.RunLine(t.Context(), 0, "snapshot"))
		assert.Equal(t, "count: 0\n", out.String())
	})

	t.Run("a snapshot that cannot be written stops the script", func(t *testing.T) {
		t.Parallel()
		exec := &script.Executor{Runner: tuikit.Start(t, counter(0)), Out: closed{}, WaitTimeout: 5 * time.Second}

		err := exec.Run(t.Context(), strings.NewReader("snapshot\nkey +\n"))
		require.ErrorIs(t, err, io.ErrClosedPipe)
		require.ErrorContains(t, err, "line 1")
	})
}

type closed struct{}

func (closed) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
