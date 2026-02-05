// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !appengine && (freebsd || darwin || dragonfly || netbsd || openbsd)
// +build !appengine
// +build freebsd darwin dragonfly netbsd openbsd

package guesstermwidth

import "syscall"

const termiosIoctlGet = syscall.TIOCGETA
