// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package script drives a tuikit.Runner from a small line-based language of
// key, wait, sleep and snapshot commands.
package script

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"unikraft.com/x/integration/tuikit"
)

// Executor runs script lines against Runner, and writes what they print to Out.
type Executor struct {
	Runner *tuikit.Runner
	Out    io.Writer

	// JSON writes one event to Out for each line, instead of only the snapshots.
	JSON bool

	// WaitTimeout is how long a wait may take, which is 5 seconds if it is zero.
	WaitTimeout time.Duration

	seq int
}

type event struct {
	Seq int `json:"seq"`

	// Line is the 1-based line number in the script/stream.
	Line int `json:"line,omitempty"`

	Cmd  string `json:"cmd"`
	Kind string `json:"kind"`

	ElapsedMS int64  `json:"elapsed_ms"`
	Error     string `json:"error,omitempty"`

	// Snapshot is included for the `snapshot` command and on errors.
	Snapshot string `json:"snapshot,omitempty"`
}

// Run runs each line of r, until the first that fails or ctx is done.
func (e *Executor) Run(ctx context.Context, r io.Reader) error {
	scanner := bufio.NewScanner(r)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		if err := e.RunLine(ctx, lineNo, scanner.Text()); err != nil {
			return fmt.Errorf("line %d: %w", lineNo, err)
		}
	}
	return scanner.Err()
}

// RunLine runs one line, which is line lineNo of its script, or 0 outside of one.
func (e *Executor) RunLine(ctx context.Context, lineNo int, raw string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd, skip, err := parseLine(raw)
	if skip && err == nil {
		return nil
	}

	start := time.Now()
	cmdText := strings.TrimSpace(raw)

	kind := "parse"
	var snapshot string
	var execErr error
	if err != nil {
		execErr = err
	} else {
		kind = cmd.Kind.String()
		snapshot, execErr = e.runCommand(ctx, cmd)
	}
	elapsed := time.Since(start)

	if e.JSON {
		e.seq++
		ev := event{
			Seq:       e.seq,
			Cmd:       cmdText,
			Kind:      kind,
			ElapsedMS: elapsed.Milliseconds(),
		}
		if lineNo > 0 {
			ev.Line = lineNo
		}
		if snapshot != "" {
			ev.Snapshot = snapshot
		}
		if execErr != nil {
			ev.Error = execErr.Error()
			ev.Snapshot = e.Runner.Snapshot()
		}
		enc := json.NewEncoder(e.Out)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(ev); err != nil {
			return err
		}
	}

	return execErr
}

func (e *Executor) runCommand(ctx context.Context, c Command) (string, error) {
	switch c.Kind {
	case CommandSleep:
		select {
		case <-time.After(c.Sleep):
			return "", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	case CommandKey:
		return "", e.Runner.PressKeys(c.Key)
	case CommandWait:
		if c.Wait == nil {
			return "", fmt.Errorf("wait expression is nil")
		}
		timeout := cmp.Or(e.WaitTimeout, 5*time.Second)
		if deadline, ok := ctx.Deadline(); ok {
			timeout = min(timeout, time.Until(deadline))
		}
		return "", e.Runner.WaitUntil(timeout, c.Wait.Eval)
	case CommandSnapshot:
		snap := e.Runner.Snapshot()
		if !e.JSON {
			_, err := fmt.Fprintln(e.Out, snap)
			return "", err
		}
		return snap, nil
	default:
		return "", fmt.Errorf("unsupported command")
	}
}
