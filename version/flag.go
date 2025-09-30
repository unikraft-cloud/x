// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package version

import (
	"fmt"

	"github.com/alecthomas/kong"
)

type VersionFlag string

func (v VersionFlag) Decode(ctx *kong.DecodeContext) error {
	return nil
}

func (v VersionFlag) IsBool() bool {
	return true
}

func (v VersionFlag) BeforeApply(app *kong.Kong, _ kong.Vars) error {
	fmt.Fprintln(app.Stdout, String())
	app.Exit(0)
	return nil
}
