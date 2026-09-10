// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"unikraft.com/x/colors"
)

// The colours are the CLI's: Primary for what is live, Warning and Error as
// such, faint for what is only there to help, and the meter's safe and warning
// shades for what the highlighter marks.
var (
	stringColor  = compat.AdaptiveColor{Light: colors.Emerald600, Dark: colors.Emerald400}
	specialColor = compat.AdaptiveColor{Light: colors.Orange600, Dark: colors.Orange400}

	promptStyle       = lipgloss.NewStyle().Foreground(colors.Primary).Bold(true)
	promptDirStyle    = lipgloss.NewStyle().Faint(true)
	continuationStyle = lipgloss.NewStyle().Faint(true)
	errorStyle        = lipgloss.NewStyle().Foreground(colors.Error)
	hintStyle         = lipgloss.NewStyle().Faint(true)

	bannerStyle = lipgloss.NewStyle().
			Foreground(colors.Warning).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colors.Warning).
			Padding(0, 1)

	// The highlighter paints what the user typed, so a tab stays a tab.
	highlightStringStyle  = lipgloss.NewStyle().Foreground(stringColor).TabWidth(lipgloss.NoTabConversion)
	highlightBuiltinStyle = lipgloss.NewStyle().Foreground(colors.Primary).Bold(true).TabWidth(lipgloss.NoTabConversion)
	highlightSpecialStyle = lipgloss.NewStyle().Foreground(specialColor).TabWidth(lipgloss.NoTabConversion)
)
