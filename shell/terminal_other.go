// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !js && !windows

package shell

import (
	"os"
	"os/signal"
	"syscall"
)

// followSize hands the terminal's size to the running command whenever it changes.
func (s *state) followSize() func() {
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			s.resize(s.measure())
		}
	}()
	return func() {
		signal.Stop(winch)
		close(winch)
	}
}
