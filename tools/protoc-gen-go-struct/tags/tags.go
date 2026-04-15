// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package tags

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// GetTags retrieves the repeated tags extension from field options.
// Returns the slice of Tag key-value pairs.  If the extension is not set or
// empty, nil is returned.
func GetTags(opts protoreflect.ProtoMessage) []*Tag {
	if opts == nil {
		return nil
	}

	ext := proto.GetExtension(opts, E_Tags)
	if ext == nil {
		return nil
	}

	tags, _ := ext.([]*Tag)
	return tags
}
