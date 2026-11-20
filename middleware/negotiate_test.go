// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestNegotiateFormat(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, accept, want string
	}{
		{"the first accepted type that is offered", "image/png, text/html, application/json", TextHTML},
		{"the first offer for any type", "*/*", AppJSON},
		{"the offer of a type with any subtype", "text/*", TextHTML},
		{"nothing for a type that only starts like an offer", "text/htmlx", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
			c.Request.Header.Set("Accept", tc.accept)
			assert.Equal(t, tc.want, NegotiateFormat(c, AppJSON, TextHTML))
		})
	}
}
