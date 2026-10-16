// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package router

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type timeoutHandler struct {
	*gin.Engine
	timeout time.Duration // none if not positive
}

// ServeHTTP wraps the embedded Gin engine's ServeHTTP function with an injected
// context which times out non-upgraded inbound requests after the timeout.
func (th timeoutHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if upgr := r.Header.Get("Upgrade"); upgr != "" || th.timeout <= 0 {
		// Upgrade to wss (probably), or no timeout at all.
		// Leave well enough alone.
		th.Engine.ServeHTTP(w, r)
		return
	}

	// Create timeout ctx.
	toCtx, cancelCtx := context.WithTimeout(
		r.Context(),
		th.timeout,
	)
	defer cancelCtx()

	// Serve the request using a shallow copy with the new context, without
	// replacing the underlying request, since the latter may be used later
	// outside of the Gin engine for post-request cleanup tasks.
	th.Engine.ServeHTTP(w, r.WithContext(toCtx))
}
