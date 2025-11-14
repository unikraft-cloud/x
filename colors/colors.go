// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package colors

import (
	"github.com/charmbracelet/lipgloss"
)

var (
	Blue50     = lipgloss.Color("#eff6ff")
	Blue100    = lipgloss.Color("#dbeafe")
	Blue200    = lipgloss.Color("#bedbff")
	Blue300    = lipgloss.Color("#8ec5ff")
	Blue400    = lipgloss.Color("#51a2ff")
	Blue500    = lipgloss.Color("#2b7fff")
	Blue600    = lipgloss.Color("#155dfc")
	Blue700    = lipgloss.Color("#1447e6")
	Blue800    = lipgloss.Color("#193cb8")
	Blue900    = lipgloss.Color("#1c398e")
	Blue950    = lipgloss.Color("#162456")
	Emerald50  = lipgloss.Color("#ecfdf5")
	Emerald100 = lipgloss.Color("#d0fae5")
	Emerald200 = lipgloss.Color("#a4f4cf")
	Emerald300 = lipgloss.Color("#5ee9b5")
	Emerald400 = lipgloss.Color("#00d492")
	Emerald500 = lipgloss.Color("#00bc7d")
	Emerald600 = lipgloss.Color("#009966")
	Emerald700 = lipgloss.Color("#007a55")
	Emerald800 = lipgloss.Color("#006045")
	Emerald900 = lipgloss.Color("#004f3b")
	Emerald950 = lipgloss.Color("#002c22")
	Orange50   = lipgloss.Color("#fff7ed")
	Orange100  = lipgloss.Color("#ffedd4")
	Orange200  = lipgloss.Color("#ffd6a7")
	Orange300  = lipgloss.Color("#ffb86a")
	Orange400  = lipgloss.Color("#ff8904")
	Orange500  = lipgloss.Color("#ff6900")
	Orange600  = lipgloss.Color("#f54900")
	Orange700  = lipgloss.Color("#ca3500")
	Orange800  = lipgloss.Color("#9f2d00")
	Orange900  = lipgloss.Color("#7e2a0c")
	Orange950  = lipgloss.Color("#441306")
	Rose50     = lipgloss.Color("#fff1f2")
	Rose100    = lipgloss.Color("#ffe4e6")
	Rose200    = lipgloss.Color("#ffccd3")
	Rose300    = lipgloss.Color("#ffa1ad")
	Rose400    = lipgloss.Color("#ff637e")
	Rose500    = lipgloss.Color("#ff2056")
	Rose600    = lipgloss.Color("#ec003f")
	Rose700    = lipgloss.Color("#c70036")
	Rose800    = lipgloss.Color("#a50036")
	Rose900    = lipgloss.Color("#8b0836")
	Rose950    = lipgloss.Color("#4d0218")
	Slate50    = lipgloss.Color("#f8fafc")
	Slate100   = lipgloss.Color("#f1f5f9")
	Slate200   = lipgloss.Color("#e2e8f0")
	Slate300   = lipgloss.Color("#cad5e2")
	Slate400   = lipgloss.Color("#90a1b9")
	Slate500   = lipgloss.Color("#62748e")
	Slate600   = lipgloss.Color("#45556c")
	Slate700   = lipgloss.Color("#314158")
	Slate800   = lipgloss.Color("#1d293d")
	Slate900   = lipgloss.Color("#0f172b")
	Slate950   = lipgloss.Color("#020618")

	Primary     = lipgloss.AdaptiveColor{Light: string(Blue500), Dark: string(Blue500)}
	PrimaryFg   = lipgloss.NewStyle().Foreground(Primary).Render
	PrimaryFgBg = lipgloss.NewStyle().
			Background(lipgloss.AdaptiveColor{Light: string(Blue100), Dark: string(Blue900)}).
			Foreground(lipgloss.AdaptiveColor{Light: string(Blue900), Dark: string(Blue100)}).
			Render
	Success     = lipgloss.AdaptiveColor{Light: string(Emerald500), Dark: string(Emerald500)}
	SuccessFg   = lipgloss.NewStyle().Foreground(Success).Render
	SuccessFgBg = lipgloss.NewStyle().
			Background(lipgloss.AdaptiveColor{Light: string(Emerald100), Dark: string(Emerald900)}).
			Foreground(lipgloss.AdaptiveColor{Light: string(Emerald900), Dark: string(Emerald100)}).
			Render
	Warning     = lipgloss.AdaptiveColor{Light: string(Orange500), Dark: string(Orange500)}
	WarningFg   = lipgloss.NewStyle().Foreground(Warning).Render
	WarningFgBg = lipgloss.NewStyle().
			Background(lipgloss.AdaptiveColor{Light: string(Orange100), Dark: string(Orange900)}).
			Foreground(lipgloss.AdaptiveColor{Light: string(Orange900), Dark: string(Orange100)}).
			Render
	Error     = lipgloss.AdaptiveColor{Light: string(Rose600), Dark: string(Rose600)}
	ErrorFg   = lipgloss.NewStyle().Foreground(Error).Render
	ErrorFgBg = lipgloss.NewStyle().
			Background(lipgloss.AdaptiveColor{Light: string(Rose100), Dark: string(Rose900)}).
			Foreground(lipgloss.AdaptiveColor{Light: string(Rose900), Dark: string(Rose100)}).
			Render
	Info     = lipgloss.AdaptiveColor{Light: string(Slate400), Dark: string(Slate400)}
	InfoFg   = lipgloss.NewStyle().Foreground(Info).Render
	InfoFgBg = lipgloss.NewStyle().
			Background(lipgloss.AdaptiveColor{Light: string(Slate100), Dark: string(Slate900)}).
			Foreground(lipgloss.AdaptiveColor{Light: string(Slate900), Dark: string(Slate100)}).
			Render
)
