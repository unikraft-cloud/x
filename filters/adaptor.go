// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026, The containerd Authors.
// Licensed under the Apache License, Version 2.0 (the "License").
// You may not use this file except in compliance with the License.

package filters

import "slices"

// Adaptor specifies the mapping of fieldpaths to a type. For the given field
// path, the value and whether the field exists should be returned. Missing
// values should return an empty value, but unknown fields should return false.
// The mapping of the fieldpath to a field is deferred to the adaptor
// implementation, but should generally follow protobuf field path/mask
// semantics.
type Adaptor interface {
	Select(fieldpath []string) (Adaptor, bool)

	Value() string
	Entries() []string
}

// AdapterFunc allows implementation specific matching of fieldpaths
type AdapterFunc func(fieldpath []string) (string, []string, bool)

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

func (f AdapterFunc) Value() string {
	value, _, ok := f(nil)
	if !ok {
		return ""
	}
	return value
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
	value   *string
	entries *[]string
	Adaptor
}

func (a *prefixAdaptor) Select(fieldpath []string) (Adaptor, bool) {
	if len(fieldpath) > 0 && a.entries == nil && a.value != nil {
		return nil, false
	}
	return a.Adaptor.Select(append(slices.Clone(a.prefix), fieldpath...))
}

func (a *prefixAdaptor) Value() string {
	if a.value != nil {
		return *a.value
	}
	exists, ok := a.Adaptor.Select(a.prefix)
	if !ok {
		return ""
	}
	return exists.Value()
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
