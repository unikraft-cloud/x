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
	"github.com/stretchr/testify/require"
)

// accepting returns the context of a request with one Accept header per value.
func accepting(accept ...string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	for _, a := range accept {
		c.Request.Header.Add("Accept", a)
	}

	return c
}

func TestNegotiateFormat(t *testing.T) {
	t.Parallel()

	offers := []string{AppJSON, TextHTML}
	for _, tc := range []struct {
		name   string
		accept []string
		want   string
	}{
		{"without an Accept header, the first offer", nil, AppJSON},
		{"the first accepted type that is offered", []string{"image/png, text/html, application/json"}, TextHTML},
		{"the types of every Accept header", []string{"image/png", TextHTML}, TextHTML},
		{"an offer whatever its parameters", []string{"text/html;q=0.8"}, TextHTML},
		{"the first offer for any type", []string{"*/*"}, AppJSON},
		{"the offer of a type with any subtype", []string{"text/*"}, TextHTML},
		{"nothing for a type that only starts like an offer", []string{"text/htmlx"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, NegotiateFormat(accepting(tc.accept...), offers...))
		})
	}
}

func TestNegotiateAccept(t *testing.T) {
	t.Parallel()

	t.Run("an offer that is accepted", func(t *testing.T) {
		t.Parallel()

		got, err := NegotiateAccept(accepting(TextHTML), AppJSON, TextHTML)
		require.NoError(t, err)
		assert.Equal(t, TextHTML, got)
	})

	t.Run("an error when nothing offered is accepted", func(t *testing.T) {
		t.Parallel()

		_, err := NegotiateAccept(accepting("image/png"), AppJSON, TextHTML)
		assert.ErrorContains(t, err, AppJSON)
	})

	t.Run("an error when nothing is offered", func(t *testing.T) {
		t.Parallel()

		_, err := NegotiateAccept(accepting(TextHTML))
		assert.Error(t, err)
	})
}
