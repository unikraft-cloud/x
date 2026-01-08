// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package oidext

// EncodeOptions controls encoding behavior.
type EncodeOption func(*encodeConfig)

type encodeConfig struct {
	// If true, fields without an `oid` tag are ignored instead of erroring.
	ignoreUntagged bool
}

func defaultDecodeConfig() *decodeConfig {
	return &decodeConfig{
		ignoreUnknown: true,
		requireAll:    false,
	}
}

// WithEncodeIgnoreUntagged causes exported struct fields without an `oid` tag
// to be ignored instead of producing an error.
func WithEncodeIgnoreUntagged() EncodeOption {
	return func(c *encodeConfig) {
		c.ignoreUntagged = true
	}
}

// DecodeOption controls decoding behavior.
type DecodeOption func(*decodeConfig)

type decodeConfig struct {
	// If true, unknown extensions under the prefix are ignored (default true).
	ignoreUnknown bool
	// If true, missing non-omitempty, non-pointer fields produce an error.
	requireAll bool
}

func defaultEncodeConfig() *encodeConfig {
	return &encodeConfig{
		ignoreUntagged: false,
	}
}

// WithDecodeIgnoreUnknown causes unknown extensions under the prefix
// to be ignored. This is the default.
func WithDecodeIgnoreUnknown() DecodeOption {
	return func(c *decodeConfig) {
		c.ignoreUnknown = true
	}
}

// WithDecodeRequireAll causes decoding to fail if any non-omitempty,
// non-pointer field is missing.
func WithDecodeRequireAll() DecodeOption {
	return func(c *decodeConfig) {
		c.requireAll = true
	}
}
