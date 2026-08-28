// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package log provides logging facilities based on [zerolog].
//
// [zerolog]: github.com/rs/zerolog
package log

import (
	"context"
	"io"
	"os"

	"github.com/rs/zerolog"
)

// G is a shorthand for FromContextOrDefault.
// It enables a logging API similar to [containerd/log].
// [containerd/log]: https://pkg.go.dev/github.com/containerd/log
var G = FromContextOrDefault

// ContextKey is how we find Loggers in a context.Context.
type ContextKey struct{}

// FromContextOrDefault returns a Logger from ctx. If no Logger is found, this
// returns the default Logger.
func FromContextOrDefault(ctx context.Context) *Logger {
	if v, ok := ctx.Value(ContextKey{}).(*Logger); ok {
		return v
	}

	return New(os.Stderr, "text", InfoLevel)
}

// WithLogger returns a new Context, derived from ctx, which carries the
// provided Logger.
func WithLogger(ctx context.Context, v *Logger) context.Context {
	return context.WithValue(ctx, ContextKey{}, v)
}

// New returns a Logger backed by a JSON or text handler.
func New(sink io.Writer, typ Type, level Level) *Logger {
	var logger zerolog.Logger

	switch typ {
	case JSONType:
		logger = zerolog.New(sink)
	case TextType:
		fallthrough
	default:
		logger = zerolog.New(zerolog.ConsoleWriter{Out: sink})
	}

	logger = logger.
		Level(level).
		With().
		Timestamp().
		Logger().
		Hook(zerolog.HookFunc(scopeHook))

	return &logger
}
