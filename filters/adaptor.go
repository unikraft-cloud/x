// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026, The containerd Authors.
// Licensed under the Apache License, Version 2.0 (the "License").
// You may not use this file except in compliance with the License.

package filters

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
)

// Adaptor specifies the mapping of fieldpaths to a type. For the given field
// path, the value and whether the field exists should be returned. Missing
// values should return an empty value, but unknown fields should return false.
// The mapping of the fieldpath to a field is deferred to the adaptor
// implementation, but should generally follow protobuf field path/mask
// semantics.
type Adaptor interface {
	Select(fieldpath []string) (Adaptor, bool)
	String() string
	Value() any
	Compare(other string) (int, bool)
	Entries() []string
}

// AdapterFunc allows implementation specific matching of fieldpaths
type AdapterFunc func(fieldpath []string) (any, []string, bool)

func (f AdapterFunc) Select(fieldpath []string) (Adaptor, bool) {
	value, entries, ok := f(fieldpath)
	if !ok {
		return nil, false
	}
	var entriesPtr *[]string
	if entries != nil {
		entriesCopy := entries
		entriesPtr = &entriesCopy
	}
	return &prefixAdaptor{
		prefix:  fieldpath,
		value:   &value,
		entries: entriesPtr,
		Adaptor: f,
	}, true
}

func (f AdapterFunc) String() string {
	value, _, ok := f(nil)
	if !ok {
		return ""
	}
	return fmt.Sprint(value)
}

func (f AdapterFunc) Value() any {
	value, _, ok := f(nil)
	if !ok {
		return nil
	}
	return value
}

func (f AdapterFunc) Compare(other string) (int, bool) {
	return compareBasic(f.Value(), other)
}

func (f AdapterFunc) Entries() []string {
	_, entries, ok := f(nil)
	if !ok {
		return nil
	}
	return entries
}

type prefixAdaptor struct {
	prefix  []string
	value   *any
	entries *[]string
	Adaptor
}

func (a *prefixAdaptor) Select(fieldpath []string) (Adaptor, bool) {
	if len(fieldpath) > 0 && a.entries == nil && a.value != nil {
		return nil, false
	}
	return a.Adaptor.Select(append(slices.Clone(a.prefix), fieldpath...))
}

func (a *prefixAdaptor) String() string {
	if a.value != nil {
		return fmt.Sprint(*a.value)
	}
	exists, ok := a.Adaptor.Select(a.prefix)
	if !ok {
		return ""
	}
	return exists.String()
}

func (a *prefixAdaptor) Value() any {
	if a.value != nil {
		return *a.value
	}
	exists, ok := a.Adaptor.Select(a.prefix)
	if !ok {
		return nil
	}
	return exists.Value()
}

func (a *prefixAdaptor) Compare(other string) (int, bool) {
	return compareBasic(a.Value(), other)
}

func (a *prefixAdaptor) Entries() []string {
	if a.entries != nil {
		return *a.entries
	}
	if a.value != nil {
		return nil
	}
	exists, ok := a.Adaptor.Select(a.prefix)
	if !ok {
		return nil
	}
	return exists.Entries()
}

func parseBasicValue(s string) any {
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}

func compareBasic(a any, other string) (int, bool) {
	b := parseBasicValue(other)
	switch av := a.(type) {
	case int64:
		if bv, ok := b.(int64); ok {
			return cmp.Compare(av, bv), true
		}
		if bv, ok := b.(float64); ok {
			return cmp.Compare(float64(av), bv), true
		}
	case float64:
		if bv, ok := b.(int64); ok {
			return cmp.Compare(av, float64(bv)), true
		}
		if bv, ok := b.(float64); ok {
			return cmp.Compare(av, bv), true
		}
	case string:
		if bv, ok := b.(string); ok {
			return cmp.Compare(av, bv), true
		}
	}
	return 0, false
}
