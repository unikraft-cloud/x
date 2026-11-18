// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"unikraft.com/x/tools/openapi-gen/generator"
	"unikraft.com/x/tools/openapi-gen/templates"
)

func main() {
	generator.Main(templates.Packs)
}
