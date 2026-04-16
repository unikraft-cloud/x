// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026, The containerd Authors.
// Licensed under the Apache License, Version 2.0 (the "License").
// You may not use this file except in compliance with the License.

package filters

// Adaptor specifies the mapping of fieldpaths to a type. For the given field
// path, the value and whether it is present should be returned. The mapping of
// the fieldpath to a field is deferred to the adaptor implementation, but
// should generally follow protobuf field path/mask semantics.
type Adaptor interface {
	Field(fieldpath []string) (value string, present bool)
}

// AdapterFunc allows implementation specific matching of fieldpaths
type AdapterFunc func(fieldpath []string) (string, bool)

// Field returns the field name and true if it exists
func (fn AdapterFunc) Field(fieldpath []string) (string, bool) {
	return fn(fieldpath)
}
