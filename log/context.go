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
	"runtime"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/rs/zerolog"
)

// G is a shorthand for FromContextOrDefault.
// It enables a logging API similar to [containerd/log].
// [containerd/log]: https://pkg.go.dev/github.com/containerd/log
var G = FromContextOrDefault

// contextKey is how we find Loggers in a context.Context.
type contextKey struct{}

// FromContextOrDefault returns a Logger from ctx. If no Logger is found, this
// returns the default Logger.
func FromContextOrDefault(ctx context.Context) *Logger {
	if v, ok := ctx.Value(contextKey{}).(*Logger); ok {
		return v
	}

	return New(os.Stderr, "text", InfoLevel)
}

// WithLogger returns a new Context, derived from ctx, which carries the
// provided Logger.
func WithLogger(ctx context.Context, v *Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, v)
}

// New returns a slog.Logger backed by a JSON or text handler.
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

	logger = logger.Level(level).With().Timestamp().Logger()
	return &logger
}

// NewWithSentry attaches a multi writer which incorporates the provided sink
// and Sentry log writer.
func NewWithSentry(sink io.Writer, typ Type, level Level, sentryCfg sentry.ClientOptions) *Logger {
	var consoleWriter io.Writer

	switch typ {
	case JSONType:
		consoleWriter = sink
	case TextType:
		fallthrough
	default:
		consoleWriter = zerolog.ConsoleWriter{Out: sink}
	}

	var levels []zerolog.Level
	if level != Disabled && level != NoLevel {
		if level <= TraceLevel {
			levels = append(levels, zerolog.TraceLevel)
		}
		if level <= DebugLevel {
			levels = append(levels, zerolog.DebugLevel)
		}
		if level <= InfoLevel {
			levels = append(levels, zerolog.InfoLevel)
		}
		if level <= WarnLevel {
			levels = append(levels, zerolog.WarnLevel)
		}
		if level <= ErrorLevel {
			levels = append(levels, zerolog.ErrorLevel)
		}
		if level <= FatalLevel {
			levels = append(levels, zerolog.FatalLevel)
		}
		if level <= PanicLevel {
			levels = append(levels, zerolog.PanicLevel)
		}
	}
	// HACK(kristoffn): This is a workaround at the moment to pass context
	// to the NewSentry function without breaking the signature of the
	// NewWithSentry function. This context object should be passed to
	// NewWithSentry as a parameter.
	ctx := context.Background()

	sentryWriter, err := NewSentry(ctx, Config{
		ClientOptions: sentryCfg,
		Options: Options{
			Levels:          levels,
			WithBreadcrumbs: true,
			FlushTimeout:    3 * time.Second,
		},
	})
	if err != nil {
		logger := New(sink, typ, level)
		logger.
			Error().
			Err(err).
			Msg("failed to create Sentry writer")
		return logger
	}

	logger := zerolog.New(zerolog.MultiLevelWriter(consoleWriter, sentryWriter)).Level(level).With().Timestamp().Logger()

	// Add a cleanup function to close the Sentry writer when the application
	// exits (in lieu of not having a `defer` statement here).
	_ = runtime.AddCleanup(&logger, func(sentryWriter *Writer) {
		sentryWriter.Close()
	}, sentryWriter)

	return &logger
}
