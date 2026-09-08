// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package log

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/rs/zerolog"

	otellog "go.opentelemetry.io/otel/log/global"
)

// Config describes the sinks a Logger writes to. Each sink filters
// independently, so a verbose sink may be paired with a quiet one.
type Config struct {
	// Sink receives the encoded log output. Defaults to os.Stderr.
	Sink io.Writer
	// Type selects the encoding written to Sink.
	Type Type
	// Level is the minimum level written to Sink.
	Level Level
	// Telemetry, when non-nil, additionally exports records over OTLP.
	Telemetry *TelemetryConfig
}

// TelemetryConfig describes the OTLP log sink.
type TelemetryConfig struct {
	// Level is the minimum level exported over OTLP.
	Level Level
}

// New returns a Logger that fans writes out to the sinks described by cfg.
// The provided context is used for extracting trace context when emitting
// OTLP records.
//
// A usable Logger is always returned, even alongside an error: it falls back
// to the sink alone, as happens when cfg.Telemetry is set but no logger
// provider has been configured by unikraft.com/x/telemetry.
func New(ctx context.Context, cfg Config) (*Logger, error) {
	sink := cfg.Sink
	if sink == nil {
		sink = os.Stderr
	}

	var console io.Writer
	switch cfg.Type {
	case JSONType:
		console = sink
	case TextType:
		fallthrough
	default:
		console = zerolog.ConsoleWriter{Out: sink}
	}

	writer, level := console, cfg.Level

	var err error
	if cfg.Telemetry != nil {
		provider := otellog.GetLoggerProvider()
		if provider == nil {
			err = fmt.Errorf("telemetry not initialized")
		} else {
			writer = zerolog.MultiLevelWriter(
				&zerolog.FilteredLevelWriter{
					Writer: zerolog.LevelWriterAdapter{Writer: console},
					Level:  cfg.Level,
				},
				&zerolog.FilteredLevelWriter{
					Writer: &otlpWriter{ctx: ctx, logger: provider.Logger("log")},
					Level:  cfg.Telemetry.Level,
				},
			)
			// Per-sink filtering only applies to events the Logger itself lets past.
			level = min(cfg.Level, cfg.Telemetry.Level)
		}
	}

	logger := zerolog.New(writer).Level(level).With().Timestamp().Logger()
	return &logger, err
}
