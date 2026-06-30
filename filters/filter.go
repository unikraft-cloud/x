// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026, The containerd Authors.
// Licensed under the Apache License, Version 2.0 (the "License").
// You may not use this file except in compliance with the License.

// Package filters defines a syntax and parser that can be used for the
// filtration of items across the containerd API. The core is built on the
// concept of protobuf field paths, with quoting.  Several operators allow the
// user to flexibly select items based on field presence, equality, inequality
// and regular expressions. Flexible adaptors support working with any type.
//
// The syntax is fairly familiar, if you've used container ecosystem
// projects.  At the core, we base it on the concept of protobuf field
// paths, augmenting with the ability to quote portions of the field path
// to match arbitrary labels. These "selectors" come in the following
// syntax:
//
// ```
// <fieldpath>[<operator><value>]
// ```
//
// A basic example is as follows:
//
// ```
// name==foo
// ```
//
// This would match all objects that have a field `name` with the value
// `foo`. If we only want to test if the field is present, we can omit the
// operator. This is most useful for matching labels in containerd. The
// following will match objects that have the field "labels" and have the
// label "foo" defined:
//
// ```
// labels.foo
// ```
//
// We also allow for quoting of parts of the field path to allow matching
// of arbitrary items:
//
// ```
// labels."very complex label"==something
// ```
//
// We also define `!=` and `~=` as operators. The `!=` will match all
// objects that don't match the value for a field and `~=` will compile the
// target value as a regular expression and match the field value against that.
//
// Selectors can be combined using a comma, such that the resulting
// selector will require all selectors are matched for the object to match.
// The following example will match objects that are named `foo` and have
// the label `bar`:
//
// ```
// name==foo,labels.bar
// ```
package filters

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Filter matches specific resources based the provided filter.
// Match returns an error if the requested field is not found.
type Filter interface {
	Match(adaptor Adaptor) (bool, error)
}

// FilterFunc is a function that handles matching with an adaptor
type FilterFunc func(Adaptor) (bool, error)

// Match matches the FilterFunc returning true if the object matches the filter
func (fn FilterFunc) Match(adaptor Adaptor) (bool, error) {
	return fn(adaptor)
}

// Always is a filter that always returns true for any type of object
var Always FilterFunc = func(adaptor Adaptor) (bool, error) {
	return true, nil
}

// Any allows multiple filters to be matched against the object
type Any []Filter

// Match returns true if any of the provided filters are true
func (m Any) Match(adaptor Adaptor) (bool, error) {
	for _, m := range m {
		matched, err := m.Match(adaptor)
		if err != nil {
			return false, err
		}
		if matched {
			return true, nil
		}
	}

	return false, nil
}

// All allows multiple filters to be matched against the object
type All []Filter

// Match only returns true if all filters match the object
func (m All) Match(adaptor Adaptor) (bool, error) {
	for _, m := range m {
		matched, err := m.Match(adaptor)
		if err != nil {
			return false, err
		}
		if !matched {
			return false, nil
		}
	}

	return true, nil
}

type operator int

const (
	operatorPresent = iota
	operatorEqual
	operatorNotEqual
	operatorMatches
	operatorNotMatches
	operatorGreater
	operatorLess
	operatorGreaterEqual
	operatorLessEqual
)

func (op operator) String() string {
	switch op {
	case operatorPresent:
		return "?"
	case operatorEqual:
		return "="
	case operatorNotEqual:
		return "!="
	case operatorMatches:
		return "~="
	case operatorNotMatches:
		return "!~="
	case operatorGreater:
		return ">"
	case operatorLess:
		return "<"
	case operatorGreaterEqual:
		return ">="
	case operatorLessEqual:
		return "<="
	}

	return "unknown"
}

type selector struct {
	fieldpath []string
	operator  operator
	value     string
	re        *regexp.Regexp
}

// ErrFieldNotFound is returned when a selector references a missing field.
var ErrFieldNotFound = &FieldNotFoundError{}

// FieldNotFoundError is returned when a selector references a missing field.
type FieldNotFoundError struct {
	Path []string
}

func (e *FieldNotFoundError) Error() string {
	if len(e.Path) == 0 {
		return "field not found"
	}
	return fmt.Sprintf("field %q not found", strings.Join(e.Path, "."))
}

func fullFieldpath(adaptor Adaptor, fieldpath []string) []string {
	if prefix, ok := adaptor.(*prefixAdaptor); ok {
		full := make([]string, 0, len(prefix.prefix)+len(fieldpath))
		full = append(full, prefix.prefix...)
		full = append(full, fieldpath...)
		return full
	}
	return fieldpath
}

func wildcardFieldpath(prefix []string, filter Filter) []string {
	path := make([]string, 0, len(prefix)+1)
	path = append(path, prefix...)
	path = append(path, "*")
	if sel, ok := filter.(selector); ok {
		path = append(path, sel.fieldpath...)
		return path
	}
	if wc, ok := filter.(wildcard); ok {
		path = append(path, wc.fieldpath...)
		return path
	}
	return path
}

func (m selector) Match(adaptor Adaptor) (bool, error) {
	root := adaptor
	adaptor, present := adaptor.Select(m.fieldpath)
	if !present {
		return false, &FieldNotFoundError{Path: fullFieldpath(root, m.fieldpath)}
	}
	value := adaptor.Value()
	entries := adaptor.Entries()
	present = value != "" || entries != nil

	switch m.operator {
	case operatorPresent:
		return present, nil
	case operatorEqual:
		return present && value == m.value, nil
	case operatorNotEqual:
		return value != m.value, nil
	case operatorMatches:
		return m.re.MatchString(value), nil
	case operatorNotMatches:
		return !m.re.MatchString(value), nil
	case operatorGreater:
		if value == "" {
			return false, nil
		}
		return compare(value, m.value, operatorGreater)
	case operatorLess:
		if value == "" {
			return false, nil
		}
		return compare(value, m.value, operatorLess)
	case operatorGreaterEqual:
		if value == "" {
			return false, nil
		}
		return compare(value, m.value, operatorGreaterEqual)
	case operatorLessEqual:
		if value == "" {
			return false, nil
		}
		return compare(value, m.value, operatorLessEqual)
	default:
		return false, nil
	}
}

type wildcard struct {
	fieldpath []string
	filter    Filter
	negated   bool // if true, use "all must not match" semantics instead of "any match"
}

func (m wildcard) Match(adaptor Adaptor) (bool, error) {
	root := adaptor
	adaptor, present := adaptor.Select(m.fieldpath)
	if !present {
		return false, &FieldNotFoundError{Path: fullFieldpath(root, m.fieldpath)}
	}
	entries := adaptor.Entries()

	if m.negated {
		// For negated operators (!=, !~=): match only if NO entry matches the positive condition
		// i.e., all entries must satisfy the negated condition.
		// Require at least one entry to exist (empty/nil shouldn't match *!=anything).
		if len(entries) == 0 {
			return false, nil
		}
		for _, entry := range entries {
			subAdaptor, ok := adaptor.Select([]string{entry})
			if !ok {
				return false, &FieldNotFoundError{Path: fullFieldpath(root, append(append([]string{}, m.fieldpath...), entry))}
			}
			matched, err := m.filter.Match(subAdaptor)
			if err != nil {
				var fieldErr *FieldNotFoundError
				if errors.As(err, &fieldErr) {
					return false, &FieldNotFoundError{Path: wildcardFieldpath(m.fieldpath, m.filter)}
				}
				return false, err
			}
			if !matched {
				return false, nil
			}
		}
		return true, nil
	}

	// For positive operators (==, ~=, present): match if ANY entry matches
	for _, entry := range entries {
		subAdaptor, ok := adaptor.Select([]string{entry})
		if !ok {
			return false, &FieldNotFoundError{Path: fullFieldpath(root, append(append([]string{}, m.fieldpath...), entry))}
		}
		matched, err := m.filter.Match(subAdaptor)
		if err != nil {
			var fieldErr *FieldNotFoundError
			if errors.As(err, &fieldErr) {
				return false, &FieldNotFoundError{Path: wildcardFieldpath(m.fieldpath, m.filter)}
			}
			return false, err
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}

// isNegatedFilter returns true if the filter uses a negated operator (!=, !~=)
// This is used to determine wildcard matching semantics.
func isNegatedFilter(f Filter) bool {
	switch v := f.(type) {
	case selector:
		return v.operator == operatorNotEqual || v.operator == operatorNotMatches
	case wildcard:
		return v.negated
	default:
		return false
	}
}
