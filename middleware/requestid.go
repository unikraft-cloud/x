// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package middleware

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"io"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	// crand provides buffered reads of random input.
	crand = bufio.NewReader(rand.Reader)
	mrand sync.Mutex

	// base32enc is a base 32 encoding based on a human-readable character set (no
	// padding).
	base32enc = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").WithPadding(-1)
)

// NewRequestID generates a new request ID string.
func NewRequestID() string {
	// 0:8  = timestamp
	// 8:12 = entropy
	//
	// inspired by ULID.
	b := make([]byte, 12)

	// Get current time in milliseconds.
	ms := uint64(time.Now().UnixMilli())

	// Store binary time data in byte buffer.
	binary.LittleEndian.PutUint64(b[0:8], ms)

	mrand.Lock()
	// Read random bits into buffer end.
	_, _ = io.ReadFull(crand, b[8:12])
	mrand.Unlock()

	// Encode the binary time+entropy ID.
	return base32enc.EncodeToString(b)
}

type requestIDContextKey struct{}

// RequestID returns the request ID associated with context. This value will
// usually be set by the request ID middleware handler, either pulling an
// existing supplied value from request headers, or generating a unique new
// entry. This is useful for tying together log entries associated with an
// original incoming request.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDContextKey{}).(string)
	return id
}

// WithRequestID stores the given request ID value and returns the wrapped
// context. See RequestID() for further information on the request ID value.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDContextKey{}, id)
}

// AddRequestID returns a gin middleware which adds a unique ID to each request
// (both response header and context).
func AddRequestID(header string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Have we found anything?
		id := c.GetHeader(header)
		if id == "" {
			// Generate new ID.
			id = NewRequestID()

			// Set the request ID in the req header in case
			// we pass the request along to another service.
			c.Request.Header.Set(header, id)
		}

		// Store request ID in new request context and set on gin ctx.
		ctx := WithRequestID(c.Request.Context(), id)
		c.Request = c.Request.WithContext(ctx)

		// Set the request ID in the rsp header.
		c.Writer.Header().Set(header, id)
	}
}
