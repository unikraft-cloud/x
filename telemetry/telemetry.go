// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package telemetry

import (
	"context"
	"errors"
	"os"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otellog "go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Init configures OTEL tracing, metrics, and logging exporters.
// Respects OTEL_SDK_DISABLED, OTEL_TRACES_EXPORTER, OTEL_METRICS_EXPORTER, and OTEL_LOGS_EXPORTER.
func Init(ctx context.Context) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	disabled, _ := strconv.ParseBool(os.Getenv("OTEL_SDK_DISABLED"))
	if disabled {
		return noop, nil
	}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return noop, nil
	}

	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithHost(),
	)
	if err != nil {
		return noop, err
	}
	if res == nil {
		res = resource.Empty()
	}

	var shutdownFuncs []func(context.Context) error

	tracesExporter := os.Getenv("OTEL_TRACES_EXPORTER")
	switch tracesExporter {
	case "", "otlp":
		traceExporter, err := otlptracehttp.New(ctx)
		if err != nil {
			return noop, err
		}
		tracerProvider := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(traceExporter),
			sdktrace.WithResource(res),
		)
		otel.SetTracerProvider(tracerProvider)
		shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)
	case "none":
		// No trace exporter
	default:
		return noop, errors.New("OTEL_TRACES_EXPORTER=" + tracesExporter + " is not supported")
	}

	metricsExporter := os.Getenv("OTEL_METRICS_EXPORTER")
	switch metricsExporter {
	case "", "otlp":
		metricExporter, err := otlpmetrichttp.New(ctx)
		if err != nil {
			// Cleanup previously initialized providers
			for _, shutdown := range shutdownFuncs {
				_ = shutdown(ctx)
			}
			return noop, err
		}
		meterProvider := sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
			sdkmetric.WithResource(res),
		)
		otel.SetMeterProvider(meterProvider)
		shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	case "none":
		// No metric exporter
	default:
		return noop, errors.New("OTEL_METRICS_EXPORTER=" + metricsExporter + " is not supported")
	}

	logsExporter := os.Getenv("OTEL_LOGS_EXPORTER")
	switch logsExporter {
	case "", "otlp":
		logExporter, err := otlploghttp.New(ctx)
		if err != nil {
			// Cleanup previously initialized providers
			for _, shutdown := range shutdownFuncs {
				_ = shutdown(ctx)
			}
			return noop, err
		}
		logProvider := sdklog.NewLoggerProvider(
			sdklog.WithResource(res),
			sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
		)
		otellog.SetLoggerProvider(logProvider)
		shutdownFuncs = append(shutdownFuncs, logProvider.Shutdown)
	case "none":
		// No log exporter
	default:
		return noop, errors.New("OTEL_LOGS_EXPORTER=" + logsExporter + " is not supported")
	}

	// Set propagator if any telemetry is enabled
	if len(shutdownFuncs) > 0 {
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		))
	}

	shutdown := func(ctx context.Context) error {
		var shutdownErr error
		for _, fn := range shutdownFuncs {
			if err := fn(ctx); err != nil {
				shutdownErr = errors.Join(shutdownErr, err)
			}
		}
		return shutdownErr
	}
	return shutdown, nil
}
