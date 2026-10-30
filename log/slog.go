// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package log

import (
	"context"
	"log/slog"
	"slices"

	"github.com/rs/zerolog"
)

// ToSlog converts a zerolog Logger to a slog Logger. Each record is forwarded
// to the Logger carried by the record's context, or to the provided Logger if
// the context carries none.
func ToSlog(l *Logger) *slog.Logger {
	return slog.New(slogHandler{l: l})
}

type slogHandler struct {
	l    *Logger
	wrap []func(slog.Handler) slog.Handler
}

func (h slogHandler) handler(ctx context.Context) slog.Handler {
	l := h.l
	if v, ok := ctx.Value(ContextKey{}).(*Logger); ok {
		l = v
	}

	var handler slog.Handler = zerolog.NewSlogHandler(*l)
	for _, wrap := range h.wrap {
		handler = wrap(handler)
	}
	return handler
}

func (h slogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.handler(ctx).Enabled(ctx, level)
}

func (h slogHandler) Handle(ctx context.Context, record slog.Record) error {
	return h.handler(ctx).Handle(ctx, record)
}

func (h slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h.with(func(handler slog.Handler) slog.Handler { return handler.WithAttrs(attrs) })
}

func (h slogHandler) WithGroup(name string) slog.Handler {
	return h.with(func(handler slog.Handler) slog.Handler { return handler.WithGroup(name) })
}

func (h slogHandler) with(wrap func(slog.Handler) slog.Handler) slogHandler {
	return slogHandler{l: h.l, wrap: append(slices.Clone(h.wrap), wrap)}
}
