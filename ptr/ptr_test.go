// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package ptr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZeroIfNil(t *testing.T) {
	tests := []struct {
		name     string
		ptr      any
		expected any
	}{
		{"nil int", (*int)(nil), 0},
		{"valid int", func() *int { x := 42; return &x }(), 42},
		{"nil string", (*string)(nil), ""},
		{"valid string", func() *string { x := "hello"; return &x }(), "hello"},
		{"nil bool", (*bool)(nil), false},
		{"valid bool", func() *bool { x := true; return &x }(), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			switch ptr := tt.ptr.(type) {
			case *int:
				result := ZeroIfNil(ptr)
				assert.Equal(t, tt.expected.(int), result)
			case *string:
				result := ZeroIfNil(ptr)
				assert.Equal(t, tt.expected.(string), result)
			case *bool:
				result := ZeroIfNil(ptr)
				assert.Equal(t, tt.expected.(bool), result)
			}
		})
	}
}

func TestErrorIfNil(t *testing.T) {
	tests := []struct {
		name        string
		ptr         any
		expected    any
		expectError bool
	}{
		{"nil int", (*int)(nil), 0, true},
		{"valid int", func() *int { x := 42; return &x }(), 42, false},
		{"nil string", (*string)(nil), "", true},
		{"valid string", func() *string { x := "hello"; return &x }(), "hello", false},
		{"nil bool", (*bool)(nil), false, true},
		{"valid bool", func() *bool { x := true; return &x }(), true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			switch ptr := tt.ptr.(type) {
			case *int:
				result, err := ErrorIfNil(ptr)
				if tt.expectError {
					require.EqualError(t, err, "value is nil")
				} else {
					require.NoError(t, err)
				}
				assert.Equal(t, tt.expected.(int), result)
			case *string:
				result, err := ErrorIfNil(ptr)
				if tt.expectError {
					require.EqualError(t, err, "value is nil")
				} else {
					require.NoError(t, err)
				}
				assert.Equal(t, tt.expected.(string), result)
			case *bool:
				result, err := ErrorIfNil(ptr)
				if tt.expectError {
					require.EqualError(t, err, "value is nil")
				} else {
					require.NoError(t, err)
				}
				assert.Equal(t, tt.expected.(bool), result)
			}
		})
	}
}

func TestCheckNotNil(t *testing.T) {
	tests := []struct {
		name        string
		params      map[string]any
		expectError bool
		errorMsg    string
	}{
		{
			name:        "all parameters valid",
			params:      map[string]any{"name": "John", "age": 30, "ptr": &struct{}{}},
			expectError: false,
		},
		{
			name:        "empty parameter map",
			params:      map[string]any{},
			expectError: false,
		},
		{
			name:        "single nil parameter",
			params:      map[string]any{"user": (*string)(nil)},
			expectError: true,
			errorMsg:    "the following parameters are nil: user",
		},
		{
			name: "multiple nil parameters",
			params: map[string]any{
				"name":    (*string)(nil),
				"config":  (map[string]string)(nil),
				"channel": (chan int)(nil),
				"valid":   "not nil",
			},
			expectError: true,
			errorMsg:    "the following parameters are nil: channel, config, name",
		},
		{
			name: "mixed types with some nil",
			params: map[string]any{
				"slice":    ([]int)(nil),
				"function": (func())(nil),
				"number":   42,
				"text":     "hello",
				"pointer":  &struct{}{},
			},
			expectError: true,
			errorMsg:    "the following parameters are nil: function, slice",
		},
		{
			name: "interface with nil pointer",
			params: map[string]any{
				"interface": func() any { var p *int; return p }(),
				"valid":     100,
			},
			expectError: true,
			errorMsg:    "the following parameters are nil: interface",
		},
		{
			name: "value types (cannot be nil)",
			params: map[string]any{
				"zero_int":     0,
				"empty_string": "",
				"false_bool":   false,
				"zero_struct":  struct{ x int }{},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckNotNil(tt.params)

			if tt.expectError {
				require.EqualError(t, err, tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestIsNil(t *testing.T) {
	tests := []struct {
		name     string
		value    any
		expected bool
	}{
		{"nil interface", nil, true},
		{"nil pointer", (*int)(nil), true},
		{"valid pointer", func() *int { x := 42; return &x }(), false},
		{"nil slice", ([]int)(nil), true},
		{"empty slice", []int{}, false},
		{"slice with elements", []int{1, 2, 3}, false},
		{"nil map", (map[string]int)(nil), true},
		{"empty map", map[string]int{}, false},
		{"map with elements", map[string]int{"key": 1}, false},
		{"nil channel", (chan int)(nil), true},
		{"valid channel", make(chan int), false},
		{"buffered channel", make(chan int, 1), false},
		{"nil function", (func())(nil), true},
		{"valid function", func() {}, false},
		{"nil interface var", func() any { var i any; return i }(), true},
		{"interface with nil pointer", func() any { var p *int; return p }(), true},
		{"interface with value", func() any { return 42 }(), false},
		{"int zero value", 0, false},
		{"int non-zero", 10, false},
		{"string empty", "", false},
		{"string non-empty", "hello", false},
		{"bool false", false, false},
		{"bool true", true, false},
		{"struct zero value", struct{ x int }{}, false},
		{"struct with values", struct{ x int }{x: 10}, false},
		{"array", [3]int{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsNil(tt.value)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestValueOrDefault(t *testing.T) {
	tests := []struct {
		name         string
		ptr          any
		defaultValue any
		expected     any
	}{
		{"nil int with default", (*int)(nil), 42, 42},
		{"valid int", func() *int { x := 10; return &x }(), 42, 10},
		{"nil string with default", (*string)(nil), "default", "default"},
		{"valid string", func() *string { x := "hello"; return &x }(), "default", "hello"},
		{"nil bool with default", (*bool)(nil), true, true},
		{"valid bool", func() *bool { x := false; return &x }(), true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			switch ptr := tt.ptr.(type) {
			case *int:
				result := ValueOrDefault(ptr, tt.defaultValue.(int))
				assert.Equal(t, tt.expected.(int), result)
			case *string:
				result := ValueOrDefault(ptr, tt.defaultValue.(string))
				assert.Equal(t, tt.expected.(string), result)
			case *bool:
				result := ValueOrDefault(ptr, tt.defaultValue.(bool))
				assert.Equal(t, tt.expected.(bool), result)
			}
		})
	}
}

func TestSafeDeref(t *testing.T) {
	tests := []struct {
		name     string
		ptr      any
		expected any
		expectOk bool
	}{
		{"nil int", (*int)(nil), 0, false},
		{"valid int", func() *int { x := 42; return &x }(), 42, true},
		{"nil string", (*string)(nil), "", false},
		{"valid string", func() *string { x := "hello"; return &x }(), "hello", true},
		{"nil bool", (*bool)(nil), false, false},
		{"valid bool", func() *bool { x := true; return &x }(), true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			switch ptr := tt.ptr.(type) {
			case *int:
				result, ok := SafeDeref(ptr)
				assert.Equal(t, tt.expectOk, ok)
				assert.Equal(t, tt.expected.(int), result)
			case *string:
				result, ok := SafeDeref(ptr)
				assert.Equal(t, tt.expectOk, ok)
				assert.Equal(t, tt.expected.(string), result)
			case *bool:
				result, ok := SafeDeref(ptr)
				assert.Equal(t, tt.expectOk, ok)
				assert.Equal(t, tt.expected.(bool), result)
			}
		})
	}
}

func TestToPtr(t *testing.T) {
	tests := []struct {
		name  string
		value int
	}{
		{"positive int", 42},
		{"zero", 0},
		{"negative int", -10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ToPtr(tt.value)

			require.NotNil(t, result)
			assert.Equal(t, tt.value, *result)
		})
	}
}

func TestFromPtr(t *testing.T) {
	tests := []struct {
		name     string
		ptr      *int
		expected int
		expectOk bool
	}{
		{"nil pointer", nil, 0, false},
		{"valid pointer", func() *int { x := 10; return &x }(), 10, true},
		{"zero value pointer", func() *int { x := 0; return &x }(), 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := FromPtr(tt.ptr)

			assert.Equal(t, tt.expectOk, ok)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNilIfZeroIntegers(t *testing.T) {
	tests := []struct {
		name     string
		value    int
		expected *int
	}{
		{"zero value", 0, nil},
		{"positive value", 42, func() *int { x := 42; return &x }()},
		{"negative value", -10, func() *int { x := -10; return &x }()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NilIfZero(tt.value)

			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				require.NotNil(t, result)
				assert.Equal(t, *tt.expected, *result)
			}
		})
	}
}

func TestNilIfZeroEmptyString(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		expected *string
	}{
		{"empty string", "", nil},
		{"non-empty string", "hello", func() *string { x := "hello"; return &x }()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NilIfZero(tt.value)

			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				require.NotNil(t, result)
				assert.Equal(t, *tt.expected, *result)
			}
		})
	}
}

func TestNilIfEqual(t *testing.T) {
	tests := []struct {
		name     string
		value    int
		compare  int
		expected *int
	}{
		{"equal values", 10, 10, nil},
		{"non-equal values", 10, 5, func() *int { x := 10; return &x }()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NilIfEqual(tt.value, tt.compare)

			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				require.NotNil(t, result)
				assert.Equal(t, *tt.expected, *result)
			}
		})
	}
}
