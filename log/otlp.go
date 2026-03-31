// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package log

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	otelLog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
)

// NewWithOTLP constructs a Logger that fans log writes to both the provided
// sink and an OTLP HTTP log exporter. Endpoint and credentials are read from
// the standard OTEL_* environment variables by the SDK.
//
// The returned cleanup function flushes and shuts down the batch processor;
// callers should defer it to avoid dropping buffered records on exit.
func NewWithOTLP(ctx context.Context, sink io.Writer, typ Type, level Level) (*Logger, func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }

	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithHost(),
	)
	if err != nil {
		New(sink, typ, level).Warn().Err(err).Msg("OTEL resource detection partial")
	}
	if res == nil {
		res = resource.Empty()
	}

	exporter, err := otlploghttp.New(ctx)
	if err != nil {
		return New(sink, typ, level), noop, err
	}

	provider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
	)

	otlpWriter := &otlpWriter{
		ctx:    ctx,
		logger: provider.Logger("log"),
	}

	var consoleWriter io.Writer
	switch typ {
	case JSONType:
		consoleWriter = sink
	default:
		consoleWriter = zerolog.ConsoleWriter{Out: sink}
	}

	logger := zerolog.New(zerolog.MultiLevelWriter(consoleWriter, otlpWriter)).
		Level(level).
		With().
		Timestamp().
		Logger()
	return &logger, provider.Shutdown, nil
}

// otlpWriter bridges zerolog's io.Writer interface to an OTel log record emitter.
type otlpWriter struct {
	ctx    context.Context
	logger otelLog.Logger
}

func (w *otlpWriter) Write(p []byte) (int, error) {
	return w.writeWithLevel(zerolog.NoLevel, p)
}

func (w *otlpWriter) WriteLevel(level zerolog.Level, p []byte) (int, error) {
	return w.writeWithLevel(level, p)
}

func (w *otlpWriter) writeWithLevel(level zerolog.Level, p []byte) (int, error) {
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(p))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		w.emitFallback(level, p)
		return len(p), nil
	}

	record := otelLog.Record{}

	if msg := readStringField(payload, zerolog.MessageFieldName, "message"); msg != "" {
		record.SetBody(otelLog.StringValue(msg))
	}

	if ts := readTimeField(payload, zerolog.TimestampFieldName); !ts.IsZero() {
		record.SetTimestamp(ts)
	} else {
		record.SetTimestamp(time.Now())
	}

	sev, sevText := severityFromPayload(level, payload)
	if sev != otelLog.SeverityUndefined {
		record.SetSeverity(sev)
	}
	if sevText != "" {
		record.SetSeverityText(sevText)
	}

	if errMsg := readStringField(payload, zerolog.ErrorFieldName); errMsg != "" {
		record.SetErr(errors.New(errMsg))
	}

	attrs := make([]otelLog.KeyValue, 0, len(payload))
	for key, value := range payload {
		if otlpSkipKey(key) {
			continue
		}
		attrs = append(attrs, otelLog.KeyValue{Key: key, Value: toOTLPValue(value)})
	}
	if len(attrs) > 0 {
		record.AddAttributes(attrs...)
	}

	w.logger.Emit(w.ctx, record)
	return len(p), nil
}

func (w *otlpWriter) emitFallback(level zerolog.Level, p []byte) {
	text := strings.TrimSpace(string(p))
	if text == "" {
		return
	}
	record := otelLog.Record{}
	record.SetBody(otelLog.StringValue(text))
	record.SetTimestamp(time.Now())
	if level != zerolog.NoLevel {
		record.SetSeverity(otlpSeverity(level))
		record.SetSeverityText(level.String())
	}
	w.logger.Emit(w.ctx, record)
}

func severityFromPayload(level zerolog.Level, payload map[string]any) (otelLog.Severity, string) {
	if level == zerolog.NoLevel {
		if text := readStringField(payload, zerolog.LevelFieldName); text != "" {
			if parsed, err := zerolog.ParseLevel(text); err == nil {
				level = parsed
			}
		}
	}
	if level == zerolog.NoLevel {
		return otelLog.SeverityUndefined, ""
	}
	return otlpSeverity(level), level.String()
}

func otlpSeverity(level zerolog.Level) otelLog.Severity {
	switch level {
	case zerolog.TraceLevel:
		return otelLog.SeverityTrace
	case zerolog.DebugLevel:
		return otelLog.SeverityDebug
	case zerolog.InfoLevel:
		return otelLog.SeverityInfo
	case zerolog.WarnLevel:
		return otelLog.SeverityWarn
	case zerolog.ErrorLevel:
		return otelLog.SeverityError
	case zerolog.FatalLevel, zerolog.PanicLevel:
		return otelLog.SeverityFatal
	default:
		return otelLog.SeverityUndefined
	}
}

func otlpSkipKey(key string) bool {
	switch key {
	case zerolog.LevelFieldName,
		zerolog.MessageFieldName,
		zerolog.TimestampFieldName,
		zerolog.ErrorFieldName:
		return true
	default:
		return false
	}
}

func readStringField(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := payload[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

func readTimeField(payload map[string]any, key string) time.Time {
	v, ok := payload[key]
	if !ok {
		return time.Time{}
	}
	s, ok := v.(string)
	if !ok {
		return time.Time{}
	}
	if ts, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return ts
	}
	if ts, err := time.Parse(time.RFC3339, s); err == nil {
		return ts
	}
	return time.Time{}
}

func toOTLPValue(value any) otelLog.Value {
	switch v := value.(type) {
	case nil:
		return otelLog.Value{}
	case string:
		return otelLog.StringValue(v)
	case bool:
		return otelLog.BoolValue(v)
	case json.Number:
		if i64, err := v.Int64(); err == nil {
			return otelLog.Int64Value(i64)
		}
		if f64, err := v.Float64(); err == nil {
			return otelLog.Float64Value(f64)
		}
		return otelLog.StringValue(v.String())
	case float64:
		if float64(int64(v)) == v {
			return otelLog.Int64Value(int64(v))
		}
		return otelLog.Float64Value(v)
	case float32:
		return otelLog.Float64Value(float64(v))
	case int:
		return otelLog.IntValue(v)
	case int64:
		return otelLog.Int64Value(v)
	case int32:
		return otelLog.Int64Value(int64(v))
	case uint:
		return otelLog.Int64Value(int64(v))
	case uint64:
		return otelLog.Int64Value(int64(v))
	case uint32:
		return otelLog.Int64Value(int64(v))
	case []any:
		values := make([]otelLog.Value, 0, len(v))
		for _, item := range v {
			values = append(values, toOTLPValue(item))
		}
		return otelLog.SliceValue(values...)
	case map[string]any:
		kvs := make([]otelLog.KeyValue, 0, len(v))
		for key, val := range v {
			kvs = append(kvs, otelLog.KeyValue{Key: key, Value: toOTLPValue(val)})
		}
		return otelLog.MapValue(kvs...)
	default:
		return otelLog.StringValue(strings.TrimSpace(
			strings.ReplaceAll(strings.ReplaceAll(fmt.Sprint(value), "\n", " "), "\t", " "),
		))
	}
}
