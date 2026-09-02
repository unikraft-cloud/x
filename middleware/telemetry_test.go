// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package middleware

import (
	"context"
	"crypto/tls"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

func TestRequestAttrs(t *testing.T) {
	for _, test := range []struct {
		name string
		req  *http.Request
		want []attribute.KeyValue
	}{{
		name: "known method",
		req:  &http.Request{Method: http.MethodPost, Host: "example.com:8080"},
		want: []attribute.KeyValue{
			semconv.HTTPRequestMethodPost,
			semconv.URLScheme("http"),
			semconv.ServerAddress("example.com"),
			semconv.ServerPort(8080),
		},
	}, {
		name: "query method",
		req:  &http.Request{Method: "QUERY", Host: "example.com"},
		want: []attribute.KeyValue{
			semconv.HTTPRequestMethodQuery,
			semconv.URLScheme("http"),
			semconv.ServerAddress("example.com"),
		},
	}, {
		name: "unknown method",
		req:  &http.Request{Method: "PROPFIND", Host: "example.com"},
		want: []attribute.KeyValue{
			semconv.HTTPRequestMethodOther,
			semconv.URLScheme("http"),
			semconv.ServerAddress("example.com"),
		},
	}, {
		name: "methods are case sensitive",
		req:  &http.Request{Method: "get", Host: "example.com"},
		want: []attribute.KeyValue{
			semconv.HTTPRequestMethodOther,
			semconv.URLScheme("http"),
			semconv.ServerAddress("example.com"),
		},
	}, {
		name: "tls",
		req:  &http.Request{Method: http.MethodGet, Host: "example.com", TLS: &tls.ConnectionState{}},
		want: []attribute.KeyValue{
			semconv.HTTPRequestMethodGet,
			semconv.URLScheme("https"),
			semconv.ServerAddress("example.com"),
		},
	}, {
		name: "ipv6 host",
		req:  &http.Request{Method: http.MethodGet, Host: "[::1]:443"},
		want: []attribute.KeyValue{
			semconv.HTTPRequestMethodGet,
			semconv.URLScheme("http"),
			semconv.ServerAddress("::1"),
			semconv.ServerPort(443),
		},
	}, {
		name: "no host",
		req:  &http.Request{Method: http.MethodGet},
		want: []attribute.KeyValue{
			semconv.HTTPRequestMethodGet,
			semconv.URLScheme("http"),
		},
	}} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, requestAttrs(test.req))
		})
	}
}

func TestRespAttrsUnmatchedRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/nope", nil)
	c.Status(http.StatusNotFound)

	assert.Equal(t, []attribute.KeyValue{
		semconv.HTTPRoute("unknown"),
		semconv.HTTPResponseStatusCode(http.StatusNotFound),
	}, respAttrs(c))
}

func TestTelemetrySpans(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))

	serve(newRouter(t), "/users/42", "/nope")

	spans := recorder.Ended()
	require.Len(t, spans, 2)

	assert.Equal(t, "GET /users/:id", spans[0].Name())
	assert.Subset(t, spans[0].Attributes(), []attribute.KeyValue{
		semconv.HTTPRequestMethodGet,
		semconv.URLScheme("http"),
		semconv.ServerAddress("example.com"),
		semconv.ServerPort(8080),
		semconv.HTTPRoute("/users/:id"),
		semconv.HTTPResponseStatusCode(http.StatusNoContent),
	})

	assert.Equal(t, "GET", spans[1].Name())
	assert.Contains(t, spans[1].Attributes(), semconv.HTTPRoute("unknown"))
}

func TestTelemetryMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	serve(newRouter(t), "/users/42", "/healthz")

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	require.Len(t, rm.ScopeMetrics, 1)
	assert.Equal(t, "unikraft.com/x/middleware", rm.ScopeMetrics[0].Scope.Name)

	want := attribute.NewSet(
		semconv.HTTPRequestMethodGet,
		semconv.URLScheme("http"),
		semconv.HTTPRoute("/users/:id"),
		semconv.HTTPResponseStatusCode(http.StatusNoContent),
		semconv.ServerAddress("example.com"),
		semconv.ServerPort(8080),
	)

	metrics := map[string]metricdata.Aggregation{}
	for _, m := range rm.ScopeMetrics[0].Metrics {
		metrics[m.Name] = m.Data
	}
	assert.Equal(t, []string{"http.server.requests"}, slices.Sorted(maps.Keys(metrics)))

	counter, ok := metrics["http.server.requests"].(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, counter.DataPoints, 1)
	assert.Equal(t, want, counter.DataPoints[0].Attributes)
	assert.Equal(t, int64(1), counter.DataPoints[0].Value)
}

// newRouter instruments a router with Telemetry, skipping "/healthz".
func newRouter(t *testing.T) *gin.Engine {
	t.Helper()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(Telemetry("^/healthz$"))
	router.GET("/users/:id", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	router.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })

	return router
}

func serve(router *gin.Engine, targets ...string) {
	for _, target := range targets {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Host = "example.com:8080"
		router.ServeHTTP(httptest.NewRecorder(), req)
	}
}
