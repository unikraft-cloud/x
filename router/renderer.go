// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package router

import (
	"context"
	"net/http"

	"github.com/a-h/templ"
	"github.com/gin-gonic/gin/render"
)

var Default = &HTMLTemplRenderer{}

type HTMLTemplRenderer struct {
	ctx      context.Context
	fallback render.HTMLRender
}

func (r *HTMLTemplRenderer) Instance(s string, d any) render.Render {
	templData, ok := d.(templ.Component)
	if !ok {
		if r.fallback != nil {
			return r.fallback.Instance(s, d)
		}
	}

	return &Renderer{
		Ctx:       r.ctx,
		Status:    -1,
		Component: templData,
	}
}

type Renderer struct {
	Ctx       context.Context
	Status    int
	Component templ.Component
}

func (t Renderer) Render(w http.ResponseWriter) error {
	t.WriteContentType(w)
	if t.Status != -1 {
		w.WriteHeader(t.Status)
	}

	if t.Component != nil {
		return t.Component.Render(t.Ctx, w)
	}

	return nil
}

func (t Renderer) WriteContentType(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
}
