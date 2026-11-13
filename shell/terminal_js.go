// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build js

package shell

// FollowSize has no SIGWINCH to follow: the size is the one measured at the start.
func FollowSize(fd uintptr, resize func(WinSize)) (stop func()) { return func() {} }
