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

// Config is a session's setup.
type Config struct {
	Instance  string
	Transport Transport
	Builtins  map[string]Builtin
	Dir       string
	Env       map[string]string
	Command   string
	Input     *os.File
}

// Streams are the three standard streams a single command is wired to.
type Streams struct {
	In       io.Reader
	Out, Err io.Writer
}

// Transport is how the shell reaches the instance.
type Transport interface {
	Exec(ctx context.Context, streams Streams, dir string, env map[string]string, args []string) (int, error)
}

// Builtin answers one ":" line here, not on the instance; args[0] is its name.
type Builtin interface {
	Run(ctx context.Context, streams Streams, args []string) (int, error)
}

// BuiltinFunc is a Builtin made of a function.
type BuiltinFunc func(ctx context.Context, streams Streams, args []string) (int, error)

func (f BuiltinFunc) Run(ctx context.Context, streams Streams, args []string) (int, error) {
	return f(ctx, streams, args)
}
