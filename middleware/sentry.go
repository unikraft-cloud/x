// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package middleware

import (
	"context"

	"github.com/getsentry/sentry-go"
	sentrygin "github.com/getsentry/sentry-go/gin"
	"github.com/gin-gonic/gin"

	"unikraft.com/x/log"
)

// Sentry initializes a Sentry client with the provided options and returns a
// gin middleware that captures errors and sends them to Sentry.
// If Sentry initialization fails, it logs a warning and returns nil.
// The middleware will automatically capture panics and errors in the request
// context, sending them to Sentry for reporting.
func Sentry(ctx context.Context, opts sentry.ClientOptions) gin.HandlerFunc {
	log.G(ctx).
		Debug().
		Str("dsn", opts.Dsn).
		Msg("initializing sentry")

	if err := sentry.Init(opts); err != nil {
		log.G(ctx).
			Warn().
			Msg("could not initialize sentry, proceeding without it")
		return nil
	}

	return sentrygin.New(sentrygin.Options{
		Repanic: true,
	})
}
