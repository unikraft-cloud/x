// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package middleware

import (
	"context"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/semconv/v1.43.0/httpconv"
	"go.opentelemetry.io/otel/trace"

	"unikraft.com/x/log"
)

// Methods absent here must report as _OTHER, so that an arbitrary method cannot
// become an unbounded metric dimension.
var requestMethods = map[string]httpconv.RequestMethodAttr{
	http.MethodConnect: httpconv.RequestMethodConnect,
	http.MethodDelete:  httpconv.RequestMethodDelete,
	http.MethodGet:     httpconv.RequestMethodGet,
	http.MethodHead:    httpconv.RequestMethodHead,
	http.MethodOptions: httpconv.RequestMethodOptions,
	http.MethodPatch:   httpconv.RequestMethodPatch,
	http.MethodPost:    httpconv.RequestMethodPost,
	http.MethodPut:     httpconv.RequestMethodPut,
	http.MethodTrace:   httpconv.RequestMethodTrace,
	"QUERY":            httpconv.RequestMethodQuery, // FIXME: use http.MethodQuery when it becomes available
}

var (
	requestMetricsOnce sync.Once
	requestCounter     metric.Int64Counter
	requestDuration    httpconv.ServerRequestDuration
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

		start := time.Now()

		ctx := c.Request.Context()
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(c.Request.Header))

		reqAttrs := requestAttrs(c.Request)

		tracer := otel.Tracer("unikraft.com/x/middleware")
		ctx, span := tracer.Start(ctx, spanName(c), trace.WithSpanKind(trace.SpanKindServer))
		span.SetAttributes(reqAttrs...)
		if _, known := requestMethods[c.Request.Method]; !known {
			span.SetAttributes(semconv.HTTPRequestMethodOriginal(c.Request.Method))
		}

		// Inject logger with trace correlation fields
		// These fields are extracted by the OTLP log writer to link logs to traces
		spanCtx := span.SpanContext()
		reqLogger := log.WithSpanContext(log.G(ctx), spanCtx)
		if requestID := RequestID(ctx); requestID != "" {
			l := reqLogger.With().Str("request_id", requestID).Logger()
			reqLogger = &l
			span.SetAttributes(attribute.String("request_id", requestID))
		}
		ctx = log.WithLogger(ctx, reqLogger)
		c.Request = c.Request.WithContext(ctx)

		c.Next()

		resAttrs := respAttrs(c)
		span.SetAttributes(resAttrs...)

		counter, duration := requestInstruments()

		metricAttrs := metric.WithAttributes(append(reqAttrs, resAttrs...)...)
		if counter != nil {
			counter.Add(ctx, 1, metricAttrs)
		}
		duration.Inst().Record(ctx, time.Since(start).Seconds(), metricAttrs)

		if len(c.Errors) > 0 {
			for _, err := range c.Errors {
				span.RecordError(err.Err)
			}
			span.SetStatus(codes.Error, c.Errors.String())
		} else if c.Writer.Status() >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(c.Writer.Status()))
		}

		span.End()
	}
}

// requestAttrs omits http.request.method_original, which is unbounded and so
// belongs only on spans.
func requestAttrs(r *http.Request) []attribute.KeyValue {
	method, known := requestMethods[r.Method]
	if !known {
		method = httpconv.RequestMethodOther
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}

	return append([]attribute.KeyValue{
		semconv.HTTPRequestMethodKey.String(string(method)),
		semconv.URLScheme(scheme),
	}, serverAttrs(r)...)
}

func serverAttrs(r *http.Request) []attribute.KeyValue {
	// server.address must exclude the port, unlike the Host header.
	addr, rawPort, err := net.SplitHostPort(r.Host)
	if err != nil {
		addr = r.Host
	}

	var attrs []attribute.KeyValue
	if addr != "" {
		attrs = append(attrs, semconv.ServerAddress(addr))
	}
	if port, err := strconv.Atoi(rawPort); err == nil && port > 0 {
		attrs = append(attrs, semconv.ServerPort(port))
	}

	return attrs
}

func respAttrs(c *gin.Context) []attribute.KeyValue {
	route := c.FullPath()
	if route == "" {
		route = "unknown"
	}

	return []attribute.KeyValue{
		semconv.HTTPRoute(route),
		semconv.HTTPResponseStatusCode(c.Writer.Status()),
	}
}

func spanName(c *gin.Context) string {
	name := "HTTP"
	if method, known := requestMethods[c.Request.Method]; known {
		name = string(method)
	}

	if route := c.FullPath(); route != "" {
		name += " " + route
	}

	return name
}

func requestInstruments() (metric.Int64Counter, httpconv.ServerRequestDuration) {
	requestMetricsOnce.Do(func() {
		meter := otel.Meter("unikraft.com/x/middleware")

		var err error
		requestDuration, err = httpconv.NewServerRequestDuration(meter)
		if err != nil {
			log.G(context.Background()).Error().Err(err).
				Msg("failed to create request duration histogram")
		}

		// NOTE: non-standard property, kept for our own backwards compat
		// we can migrate our consumers to the http.server.request.duration histogram
		requestCounter, err = meter.Int64Counter(
			"http.server.requests",
			metric.WithDescription("Number of HTTP requests"),
			metric.WithUnit("1"),
		)
		if err != nil {
			log.G(context.Background()).Error().Err(err).
				Msg("failed to create request counter")
			requestCounter = nil
		}
	})

	return requestCounter, requestDuration
}
