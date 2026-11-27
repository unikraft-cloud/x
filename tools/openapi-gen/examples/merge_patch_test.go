// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package examples_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/tools/openapi-gen/examples/client"
	"unikraft.com/x/tools/openapi-gen/examples/server"
)

type patchWidgets struct {
	server.Widgets
	contentType string
}

func (w *patchWidgets) PatchWidget(g *gin.Context, id string, req *server.PatchWidgetRequest) (*server.GetWidgetResponse, int, error) {
	w.contentType = g.ContentType()
	return &server.GetWidgetResponse{
		Data: &server.GetWidgetResponseData{
			Widget: &server.Widget{Id: &id, Name: req.Name},
		},
	}, http.StatusOK, nil
}

func TestMergePatchRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	svc := &patchWidgets{}
	server.RegisterWidgets(engine, svc, nil)
	srv := httptest.NewServer(engine)
	defer srv.Close()

	c, err := client.NewClient(srv.URL)
	require.NoError(t, err)

	resp, err := c.Widgets.PatchWidget(t.Context(), "w1", &client.PatchWidgetRequest{Name: new("renamed")})
	require.NoError(t, err)

	assert.Equal(t, "application/merge-patch+json", svc.contentType)
	require.NotNil(t, resp.Data)
	require.NotNil(t, resp.Data.Widget)
	assert.Equal(t, "w1", *resp.Data.Widget.Id)
	assert.Equal(t, "renamed", *resp.Data.Widget.Name)
}
