// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package middleware

import (
	"context"
	"net/http"
	"regexp"
	"sync"

	"github.com/gin-gonic/gin"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"unikraft.com/x/log"
)

var (
	requestMetricsOnce sync.Once
	requestCounter     metric.Int64Counter
	requestCounterErr  error
)

// Telemetry creates a span per request, injects a correlated logger,
// and emits request metrics.
func Telemetry(skipPaths ...string) gin.HandlerFunc {
	var regs []*regexp.Regexp
	for _, p := range skipPaths {
		regs = append(regs, regexp.MustCompile(p))
	}

	return func(c *gin.Context) {
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery
		if raw != "" {
			path = path + "?" + raw
		}

		for _, reg := range regs {
			if reg.MatchString(path) {
				c.Next()
				return
			}
		}

		ctx := c.Request.Context()
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(c.Request.Header))

		tracer := otel.Tracer("unikraft.com/x/middleware")
		ctx, span := tracer.Start(ctx, "http.request", trace.WithSpanKind(trace.SpanKindServer))
		span.SetAttributes(
			attribute.String("http.method", c.Request.Method),
			attribute.String("http.host", c.Request.Host),
		)

		// Inject logger with trace correlation fields
		// These fields are extracted by the OTLP log writer to link logs to traces
		spanCtx := span.SpanContext()
		reqLogger := log.WithSpanContext(log.G(ctx), spanCtx)
		if requestID := RequestID(ctx); requestID != "" {
			l := reqLogger.With().Str("request_id", requestID).Logger()
			reqLogger = &l
		}
		ctx = log.WithLogger(ctx, reqLogger)
		c.Request = c.Request.WithContext(ctx)

		c.Next()

		status := c.Writer.Status()
		route := c.FullPath()
		if route == "" {
			route = "unknown"
		}

		attrs := []attribute.KeyValue{
			attribute.String("http.method", c.Request.Method),
			attribute.String("http.host", c.Request.Host),
			attribute.String("http.route", route),
			attribute.Int("http.status_code", status),
		}

		if counter := requestCounterInstrument(); counter != nil {
			counter.Add(ctx, 1, metric.WithAttributes(attrs...))
		}

		span.SetAttributes(
			attribute.String("http.route", route),
			attribute.Int("http.status_code", status),
		)
		span.SetName(c.Request.Method + " " + route)

		if len(c.Errors) > 0 {
			for _, err := range c.Errors {
				span.RecordError(err.Err)
			}
			span.SetStatus(codes.Error, c.Errors.String())
		} else if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(status))
		}

		span.End()
	}
}

func requestCounterInstrument() metric.Int64Counter {
	requestMetricsOnce.Do(func() {
		meter := otel.Meter("unikraft.com/x/middleware")
		requestCounter, requestCounterErr = meter.Int64Counter(
			"http.server.requests",
			metric.WithDescription("Number of HTTP requests"),
			metric.WithUnit("1"),
		)
		if requestCounterErr != nil {
			log.G(context.Background()).Error().Err(requestCounterErr).
				Msg("failed to create request counter")
			requestCounter = nil
		}
	})

	return requestCounter
}
