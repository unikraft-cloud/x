// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"unikraft.com/x/tools/openapi-gen/examples/server"
)

type stubWidgets struct{}

func (stubWidgets) CreateWidget(g *gin.Context, req *server.CreateWidgetRequest) (*server.CreateWidgetResponse, int, error) {
	return &server.CreateWidgetResponse{}, http.StatusOK, nil
}

func (stubWidgets) DeleteWidget(g *gin.Context, id string) (*server.DeleteWidgetResponse, int, error) {
	return &server.DeleteWidgetResponse{}, http.StatusOK, nil
}

func (stubWidgets) GetWidget(g *gin.Context, id string) (*server.GetWidgetResponse, int, error) {
	return &server.GetWidgetResponse{}, http.StatusOK, nil
}

func (stubWidgets) ListWidgets(g *gin.Context, limit *int) (*server.ListWidgetsResponse, int, error) {
	return &server.ListWidgetsResponse{}, http.StatusOK, nil
}

func (stubWidgets) WatchWidget(g *gin.Context, cancel context.CancelFunc, id string) <-chan *server.Widget {
	ch := make(chan *server.Widget)
	close(ch)
	return ch
}

func noopResp(c *gin.Context, code int, v any) {
	c.Status(code)
}

// marker returns middleware recording name into calls, to assert chain order without relying on gin's HandlerNames.
func marker(calls *[]string, name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		*calls = append(*calls, name)
		c.Next()
	}
}

// TestRegisterWidgets_BackwardCompatible pins RegisterWidgets' pre-existing shared-middleware-for-every-route behavior.
func TestRegisterWidgets_BackwardCompatible(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var calls []string
	engine := gin.New()
	server.RegisterWidgets(engine, stubWidgets{}, noopResp, marker(&calls, "shared"))

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/widgets", nil),
		httptest.NewRequest(http.MethodGet, "/widgets/1", nil),
		httptest.NewRequest(http.MethodGet, "/widgets", nil),
	} {
		calls = nil
		engine.ServeHTTP(httptest.NewRecorder(), req)
		assert.Equal(t, []string{"shared"}, calls, "%s %s", req.Method, req.URL.Path)
	}
}

// TestWidgetsRegistration_OperationMiddleware verifies WithOperationMiddleware runs only for its own operation, after shared middleware.
func TestWidgetsRegistration_OperationMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var calls []string
	engine := gin.New()
	server.NewWidgetsRegistration(stubWidgets{}, noopResp).
		WithMiddleware(marker(&calls, "shared")).
		WithOperationMiddleware(server.OpCreateWidget, marker(&calls, "create-only")).
		Register(engine)

	calls = nil
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/widgets", nil))
	assert.Equal(t, []string{"shared", "create-only"}, calls, "operation middleware should run after shared middleware")

	calls = nil
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/widgets/1", nil))
	assert.Equal(t, []string{"shared"}, calls, "operation middleware must not leak into other operations")
}
