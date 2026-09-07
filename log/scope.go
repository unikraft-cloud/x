// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package log

import (
	"context"
	"slices"

	"github.com/rs/zerolog"
)

type (
	// stores the scope for this logger as a `string`
	scopeContextKey struct{}

	// stores the scope filter for this logger as a `[]string`
	scopeFilterContextKey struct{}
)

// WithScopedLogger returns a new context, derived from [ctx], which carries a
// sub logger of the one embedded in [ctx] or a new default logger, configured
// to include [scope].
func WithScopedLogger(ctx context.Context, scope string) context.Context {
	return WithLogger(ctx, Scoped(FromContextOrDefault(ctx), scope))
}

// Scoped returns a sub logger configured to include [scope].
func Scoped(logger *Logger, scope string) *Logger {
	// attach scope
	l := updateLogCtx(*logger, func(ctx context.Context) context.Context {
		return context.WithValue(ctx, scopeContextKey{}, scope)
	})
	return &l
}

// FilterScope returns a sub logger configured to omit log entries when the
// [filter] matches the scope carried by the entry.
func FilterScope(logger *Logger, filter []string) *Logger {
	// attach scope filter
	l := updateLogCtx(*logger, func(ctx context.Context) context.Context {
		return context.WithValue(ctx, scopeFilterContextKey{}, filter)
	})
	return &l
}

// updateLogCtx retrieves the context which is propagated to events generated
// by the [logger] and applies [update] to it.
func updateLogCtx(logger zerolog.Logger, update func(context.Context) context.Context) zerolog.Logger {
	// get logger context
	ctx := logger.Log().GetCtx()

	// update
	ctx = update(ctx)

	// update logger context
	return logger.With().Ctx(ctx).Logger()
}

// scopeHook is the [zerolog.HookFunc] for enriching log events with a scope
// field and filtering log events by scope (if set)
func scopeHook(e *zerolog.Event, _ zerolog.Level, _ string) {
	ctx := e.GetCtx()

	if scope, ok := ctx.Value(scopeContextKey{}).(string); ok {
		// include scope field if set
		e = e.Str("scope", scope)

		if filter, ok := ctx.Value(scopeFilterContextKey{}).([]string); ok {
			if !slices.Contains(filter, scope) {
				// discard event if filter set and not matching
				e.Discard()
			}
		}
	}
}
