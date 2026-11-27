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

// FollowSize hands resize the size of the terminal at fd at once and whenever it changes, until stopped.
func FollowSize(fd uintptr, resize func(WinSize)) (stop func()) {
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		// The size may have changed before Notify.
		resize(sizeOf(fd))
		for range winch {
			resize(sizeOf(fd))
		}
	}()
	return func() {
		signal.Stop(winch)
		close(winch)
	}
}
