// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import "time"

// FollowSize hands resize the size of the console at fd at once and whenever it changes, until stopped.
// Windows has no SIGWINCH, so it polls.
func FollowSize(fd uintptr, resize func(WinSize)) (stop func()) {
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		var last WinSize
		for {
			if size := sizeOf(fd); size != last {
				last = size
				resize(size)
			}
			select {
			case <-done:
				return
			case <-tick.C:
			}
		}
	}()
	return func() { close(done) }
}
