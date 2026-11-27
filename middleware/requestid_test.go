// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestAddRequestID(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, sent string
		kept       bool
	}{
		{"an ID from the client is kept", "0a1b-2c3d.4e5f:6", true},
		{"no ID gets a new one", "", false},
		{"an ID with other characters is replaced", "<script>", false},
		{"an ID longer than 64 characters is replaced", strings.Repeat("a", 65), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
			c.Request.Header.Set("X-Request-ID", tc.sent)
			AddRequestID("X-Request-ID")(c)

			id := RequestID(c.Request.Context())
			assert.Equal(t, id, w.Header().Get("X-Request-ID"))
			if tc.kept {
				assert.Equal(t, tc.sent, id)
			} else {
				assert.NotEqual(t, tc.sent, id)
				assert.Regexp(t, clientRequestIDRe, id)
			}
		})
	}
}
