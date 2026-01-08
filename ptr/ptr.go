// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package ptr provides utility functions for working with pointers.
package ptr

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Ptr returns a pointer to the value passed in.
//
// Deprecated: Ptr exists for historical compatibility and should not be used.
// Please use ToPtr instead.
func Ptr[t any](v t) *t {
	return &v
}

// ZeroIfNil returns the zero value of type T if the pointer is nil,
// otherwise returns the dereferenced value of the pointer.
//
// Example:
//
//	var p *int = nil
//	result := ZeroIfNil(p) // returns 0
//
//	x := 42
//	result := ZeroIfNil(&x) // returns 42
func ZeroIfNil[T any](val *T) T {
	if val == nil {
		var zero T
		return zero
	}
	return *val
}

// ErrorIfNil returns the dereferenced value of the pointer if it's not nil,
// otherwise returns an error indicating that the value was nil.
//
// Example:
//
//	var p *int = nil
//	result, err := ErrorIfNil(p) // returns 0, "value is nil"
//
//	x := 42
//	result, err := ErrorIfNil(&x) // returns 42, nil
func ErrorIfNil[T any](val *T) (T, error) {
	if val == nil {
		var zero T
		return zero, errors.New("value is nil")
	}
	return *val, nil
}

// CheckNotNil checks if any of the provided parameters are nil and returns
// an error listing the nil parameter names.
func CheckNotNil(params map[string]any) error {
	var nilParams []string

	for name, value := range params {
		if IsNil(value) {
			nilParams = append(nilParams, name)
		}
	}

	if len(nilParams) > 0 {
		sort.Strings(nilParams)
		return fmt.Errorf("the following parameters are nil: %s", strings.Join(nilParams, ", "))
	}

	return nil
}

// IsNil returns true if the value is nil, handling all types that can be nil in Go
// (pointers, slices, maps, channels, functions, interfaces).
//
// Example:
//
//	var ptr *int        // IsNil(ptr) returns true
//	var num int = 0     // IsNil(num) returns false
func IsNil(value any) bool {
	if value == nil {
		return true
	}

	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func, reflect.Interface:
		return v.IsNil()
	default:
		return false
	}
}

// ValueOrDefault returns the dereferenced value if the pointer is not nil,
// otherwise returns the provided default value.
func ValueOrDefault[T any](ptr *T, defaultValue T) T {
	if ptr == nil {
		return defaultValue
	}
	return *ptr
}

// SafeDeref safely dereferences a pointer, returning the value and a boolean
// indicating whether the pointer was non-nil.
func SafeDeref[T any](ptr *T) (T, bool) {
	if ptr == nil {
		var zero T
		return zero, false
	}
	return *ptr, true
}

// ToPtr converts a value to a pointer.
// Returns a pointer to the value.
func ToPtr[T any](value T) *T {
	return &value
}

// FromPtr converts a pointer to an optional-style (value, ok) pair.
// Returns (zero, false) if ptr is nil, otherwise (value, true).
func FromPtr[T any](ptr *T) (T, bool) {
	if ptr == nil {
		var zero T
		return zero, false
	}
	return *ptr, true
}

// NilIfZero returns a pointer to the value if it is non-zero, otherwise returns
// nil.
//
// Example:
//
//	v := NilIfZero(5) // v points to 5
//
//	v = NilIfZero(0) // v is nil
func NilIfZero[T comparable](s T) *T {
	var zero T
	if s == zero {
		return nil
	}
	return &s
}

// NilIfEqual returns a pointer to the value if it is not equal to the compare
// value, otherwise returns nil.
//
// Example:
//
//	v := NilIfEqual(10, 0) // v points to 10
//
//	v2 := NilIfEqual(0, 0) // v2 is nil
//
//	v3 := NilIfEqual("foo", "bar") // v3 points to "foo"
//
//	v4 := NilIfEqual("foo", "foo") // v4 is nil
func NilIfEqual[T comparable](value, compare T) *T {
	if value == compare {
		return nil
	}

	return &value
}
