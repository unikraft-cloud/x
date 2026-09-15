// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"charm.land/lipgloss/v2"
	"unikraft.com/x/colors"
)

var errorStyle = lipgloss.NewStyle().Foreground(colors.Error)
