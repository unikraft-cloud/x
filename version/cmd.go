// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package version

import (
	"fmt"

	"github.com/alecthomas/kong"
)

// VersionCmd is a kong command that prints detailed version information.
type VersionCmd struct{}

func (c *VersionCmd) Run(ctx *kong.Context) error {
	fmt.Fprintln(ctx.Stdout, Long())
	return nil
}
