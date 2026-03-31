// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package kingkong

import "github.com/alecthomas/kong"

func DescriptionDetail(description string) kong.Option {
	return kong.PostBuild(func(k *kong.Kong) error {
		k.Model.Detail = description
		return nil
	})
}
