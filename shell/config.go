// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"context"
	"os"

	"unikraft.com/x/stdio"
)

// Config is a session's setup.
type Config struct {
	Instance       string
	Transport      Transport
	Builtins       map[string]Builtin
	Dir            string
	Env            map[string]string
	Command        string
	Input          *os.File
	Banner         string
	SuspendSignals SuspendFunc
}

// SuspendFunc lends the shell a signal for as long as it holds the prompt; x/signal's Signals.Suspend is one.
type SuspendFunc func(sig ...os.Signal) (restore func())

// Builtin answers one ":" line here, not on the instance; args[0] is its name.
type Builtin interface {
	Run(ctx context.Context, streams stdio.Stdio, args []string) (int, error)
}

// BuiltinFunc is a Builtin made of a function.
type BuiltinFunc func(ctx context.Context, streams stdio.Stdio, args []string) (int, error)

func (f BuiltinFunc) Run(ctx context.Context, streams stdio.Stdio, args []string) (int, error) {
	return f(ctx, streams, args)
}
