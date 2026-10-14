// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"fmt"
	"io"
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"

	"unikraft.com/x/stdio"
)

// measure is the terminal's size, read off the output: windows sizes only that handle.
func (s *state) measure() WinSize {
	var size WinSize
	if out, ok := s.console.Out.(interface{ Fd() uintptr }); ok {
		size.Cols, size.Rows, _ = term.GetSize(out.Fd())
	}
	return size
}

// resize records the size and hands it to the command on the terminal, dropping
// one it has not read yet.
func (s *state) resize(size WinSize) {
	s.sizeMu.Lock()
	defer s.sizeMu.Unlock()

	s.terminal, s.size = true, size
	if s.resized == nil {
		return
	}
	select {
	case <-s.resized:
	default:
	}
	s.resized <- size
}

// lendTerminal is the session's terminal for a command that has it to itself,
// nil for any other; release hands it back.
func (s *state) lendTerminal(streams stdio.Stdio) (*Terminal, func()) {
	none := func() {}
	if streams.Stdin != io.Reader(s.stdin) || streams.Stdout != s.console.Out ||
		(streams.Stderr != s.console.Err && streams.Stderr != s.console.Out) {
		return nil, none
	}

	s.sizeMu.Lock()
	defer s.sizeMu.Unlock()
	if !s.terminal {
		return nil, none
	}

	var was *term.State
	if s.tty != nil {
		var err error
		if was, err = term.MakeRaw(s.tty.Fd()); err != nil {
			return nil, none
		}
	}
	s.resized = make(chan WinSize, 1)
	lent := &Terminal{Size: s.size, Resize: s.resized}
	return lent, sync.OnceFunc(func() {
		s.sizeMu.Lock()
		s.resized = nil
		s.sizeMu.Unlock()
		if was == nil {
			return
		}
		_ = term.Restore(s.tty.Fd(), was)
		// A full-screen program the ^C killed may have left its keyboard mode on.
		fmt.Fprint(s.console.Out, ansi.KittyKeyboard(0, 1), ansi.ResetModifyOtherKeys)
	})
}
