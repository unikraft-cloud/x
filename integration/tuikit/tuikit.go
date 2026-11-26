// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package tuikit provides a small harness for driving Bubble Tea models.
//
// Unlike unit-test "step" harnesses that execute commands synchronously,
// Runner behaves like a tea.Program: Update is called synchronously when you
// send messages, but any returned Cmds execute asynchronously and feed their
// resulting messages back into Update.
//
// This is a good fit for testing and previewing the TUI, because the real TUI
// uses asynchronous commands (refresh ticks, background readers, API calls).
package tuikit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Runner drives a Bubble Tea model and exposes snapshot-oriented helpers.
//
// Create a Runner with New, send input with PressKeys, and use WaitUntil to
// block until the rendered snapshot matches a condition.
//
// Always call Stop when you're done to avoid goroutine leaks.
type Runner struct {
	model  tea.Model
	width  int
	height int

	mu sync.RWMutex

	updatedCh chan struct{}

	msgCh  chan queuedMsg
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// New starts a Runner and immediately schedules model.Init().
//
// width/height are applied by sending a tea.WindowSizeMsg; snapshots are
// truncated to these dimensions.
func New(model tea.Model, width, height int) *Runner {
	runner := &Runner{
		model:     model,
		width:     width,
		height:    height,
		updatedCh: make(chan struct{}, 1),
		msgCh:     make(chan queuedMsg, 128),
		stopCh:    make(chan struct{}),
	}
	// Run like a tea.Program by default.
	runner.wg.Go(runner.loop)

	if width > 0 || height > 0 {
		runner.send(tea.WindowSizeMsg{Width: width, Height: height})
	}

	runner.mu.RLock()
	initCmd := runner.model.Init()
	runner.mu.RUnlock()
	runner.scheduleCmd(initCmd)

	return runner
}

// PressKeys sends one or more key press messages.
//
// Note: this only guarantees Update has run. Any Cmds returned by Update will
// execute asynchronously; use WaitUntil to wait for the UI to settle.
func (r *Runner) PressKeys(keys ...tea.Key) {
	for _, key := range keys {
		r.send(tea.KeyPressMsg(key))
	}
}

// Snapshot returns the current view as plain text.
//
// The snapshot is truncated to the Runner's width/height and cleaned of ANSI
// escape sequences.
func (r *Runner) Snapshot() string {
	return ansi.Strip(r.view())
}

// WaitUntil blocks until pred returns true for the current Snapshot or the
// timeout expires.
//
// interval controls optional polling in addition to update-driven checks. Set
// interval to 0 to only re-check on model updates.
func (r *Runner) WaitUntil(timeout, interval time.Duration, pred func(snapshot string) bool) error {
	if pred == nil {
		return errors.New("nil predicate")
	}

	if pred(r.Snapshot()) {
		return nil
	}
	if timeout <= 0 {
		return context.DeadlineExceeded
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var tickerC <-chan time.Time
	if interval > 0 {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		tickerC = ticker.C
	}

	for {
		select {
		case <-r.updatedCh:
		case <-tickerC:
		case <-timer.C:
			return context.DeadlineExceeded
		case <-r.stopCh:
			return context.Canceled
		}

		if pred(r.Snapshot()) {
			return nil
		}
	}
}

// WaitUntilSnapshot is like WaitUntil, but returns the exact snapshot that
// satisfied pred.
func (r *Runner) WaitUntilSnapshot(timeout, interval time.Duration, pred func(snapshot string) bool) (string, error) {
	var snap string
	err := r.WaitUntil(timeout, interval, func(s string) bool {
		if pred == nil {
			return false
		}
		if pred(s) {
			snap = s
			return true
		}
		return false
	})
	if err != nil {
		return "", err
	}
	return snap, nil
}

// Stop terminates the runner loop and waits for in-flight commands.
func (r *Runner) Stop() {
	r.stop()
	r.wg.Wait()
}

type queuedMsg struct {
	msg tea.Msg
	ack chan struct{}
}

func (r *Runner) send(msg tea.Msg) {
	ack := make(chan struct{})
	select {
	case r.msgCh <- queuedMsg{msg: msg, ack: ack}:
		select {
		case <-ack:
		case <-r.stopCh:
			return
		}
	case <-r.stopCh:
		return
	}
}

func (r *Runner) view() string {
	r.mu.RLock()
	view := r.model.View()
	r.mu.RUnlock()
	lines := strings.Split(view.Content, "\n")
	if r.height > 0 && len(lines) > r.height {
		lines = lines[:r.height]
	}
	if r.width > 0 {
		for i := range lines {
			lines[i] = ansi.Truncate(lines[i], r.width, "")
		}
	}
	return strings.Join(lines, "\n")
}

func (r *Runner) stop() {
	select {
	case <-r.stopCh:
		// already stopped
	default:
		close(r.stopCh)
	}
}

func (r *Runner) loop() {
	for {
		select {
		case qm := <-r.msgCh:
			r.handleMsg(qm.msg)
			if qm.ack != nil {
				close(qm.ack)
			}
		case <-r.stopCh:
			return
		}
	}
}

func (r *Runner) handleMsg(msg tea.Msg) {
	if msg == nil {
		return
	}

	// Program-level messages.
	switch m := msg.(type) {
	case tea.BatchMsg:
		for _, cmd := range m {
			r.scheduleCmd(cmd)
		}
		return
	case tea.QuitMsg:
		// Stop processing further messages.
		r.stop()
		return
	}

	r.mu.Lock()
	model, cmd := r.model.Update(msg)
	r.model = model
	r.mu.Unlock()
	r.signalUpdated()

	r.scheduleCmd(cmd)
}

func (r *Runner) signalUpdated() {
	select {
	case r.updatedCh <- struct{}{}:
	default:
	}
}

func (r *Runner) scheduleCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}

	r.wg.Go(func() {
		msg := cmd()
		if msg == nil {
			return
		}
		select {
		case r.msgCh <- queuedMsg{msg: msg}:
		case <-r.stopCh:
			return
		}
	})
}
