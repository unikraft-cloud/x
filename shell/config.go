// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"context"
	"io"
	"os"
)

// What a caller hands the shell: the instance to reach, the builtins to answer
// with, and the streams to talk on.

// Config is a session's setup.
type Config struct {
	Instance  string
	Transport Transport

	// Builtins answer the lines that open with ":", by name; nil is none.
	Builtins map[string]Builtin

	Dir     string
	Env     map[string]string
	Command string

	// Banner is what the prompt opens with, a line each.
	Banner []string

	// SuspendSignals takes SIGINT away from the caller while the prompt holds
	// it, typically (*signal.Signals).Suspend of unikraft.com/x/signal. Left
	// nil, a ^C during a command cancels the caller's context and ends the
	// session instead of the command.
	SuspendSignals SuspendFunc
}

// SuspendFunc lends the shell a signal for as long as it holds the prompt.
type SuspendFunc func(sig ...os.Signal) (restore func())

// Streams are the three standard streams a single command is wired to.
type Streams struct {
	In       io.Reader
	Out, Err io.Writer
}

// Transport is how the shell reaches the instance.
//
// Exec runs args on the instance, in dir and with env, wired to streams that
// must be live: Out and Err written as the command produces them, In read while
// it runs.
type Transport interface {
	Exec(ctx context.Context, streams Streams, dir string, env map[string]string, args []string) (int, error)
}

// Builtin answers one ":" line here rather than on the instance; args[0] is its name.
type Builtin interface {
	Run(ctx context.Context, streams Streams, args []string) (int, error)
}

// BuiltinFunc is a Builtin made of a function.
type BuiltinFunc func(ctx context.Context, streams Streams, args []string) (int, error)

func (f BuiltinFunc) Run(ctx context.Context, streams Streams, args []string) (int, error) {
	return f(ctx, streams, args)
}
