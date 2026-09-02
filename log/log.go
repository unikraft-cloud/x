// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package log

import (
	"fmt"

	"github.com/rs/zerolog"
)

// Type aliases.
type (
	Level  = zerolog.Level
	Logger = zerolog.Logger
)

const (
	// DebugLevel defines debug log level.
	DebugLevel Level = iota
	// InfoLevel defines info log level.
	InfoLevel
	// WarnLevel defines warn log level.
	WarnLevel
	// ErrorLevel defines error log level.
	ErrorLevel
	// FatalLevel defines fatal log level.
	FatalLevel
	// PanicLevel defines panic log level.
	PanicLevel
	// NoLevel defines an absent log level.
	NoLevel
	// Disabled disables the logger.
	Disabled

	// TraceLevel defines trace log level.
	TraceLevel Level = -1
	// Values less than TraceLevel are handled as numbers.
)

type Type string

const (
	// Machine-readable output.
	JSONType Type = "json"
	// Human-readable output.
	TextType Type = "text"
)

// UnmarshalText implements encoding.TextUnmarshaler.
func (t *Type) UnmarshalText(text []byte) error {
	switch v := Type(text); v {
	case JSONType, TextType:
		*t = v
		return nil
	default:
		return fmt.Errorf("unknown log type: %q", text)
	}
}

// MarshalText implements encoding.TextMarshaler.
func (t Type) MarshalText() ([]byte, error) {
	return []byte(t), nil
}
