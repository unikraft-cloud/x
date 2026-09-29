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

	logger, err := New(ctx, Config{Type: TextType, Level: InfoLevel})
	if err != nil {
		logger.Warn().Err(err).Msg("could not fully construct the default logger")
	}
	return logger
}

// WithLogger returns a new Context, derived from ctx, which carries the
// provided Logger.
func WithLogger(ctx context.Context, v *Logger) context.Context {
	return context.WithValue(ctx, ContextKey{}, v)
}
