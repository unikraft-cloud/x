// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package middleware

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"unikraft.com/x/log"
)

// Logger logs the request.
func Logger(ctx context.Context, skipPaths ...string) gin.HandlerFunc {
	var regs []*regexp.Regexp
	for _, p := range skipPaths {
		regs = append(regs, regexp.MustCompile(p))
	}

	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery
		if raw != "" {
			path = path + "?" + raw
		}

		c.Next()

		for _, reg := range regs {
			if !reg.MatchString(path) {
				continue
			}

			return
		}

		end := time.Now()
		latency := end.Sub(start)

		l := log.G(ctx).Info()

		msg := "request"
		if len(c.Errors) > 0 {
			l = log.G(ctx).Error()
			msg = strings.Join(c.Errors.Errors(), ": ")
		}

		l.
			Int("status", c.Writer.Status()).
			Str("method", c.Request.Method).
			Str("path", path).
			Str("ip", c.ClientIP()).
			Dur("latency", latency).
			Str("user_agent", c.Request.UserAgent()).
			Int("body_size", c.Writer.Size()).
			Msg(msg)
	}
}
