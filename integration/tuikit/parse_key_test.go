// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package tuikit_test

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/integration/tuikit"
)

func TestParseKey(t *testing.T) {
	t.Parallel()

	for spec, want := range map[string]tea.Key{
		"enter":        {Code: tea.KeyEnter},
		"Esc":          {Code: tea.KeyEscape},
		"r":            {Code: 'r', Text: "r"},
		"G":            {Code: 'G', Text: "G"},
		"?":            {Code: '?', Text: "?"},
		"+":            {Code: '+', Text: "+"},
		"ö":            {Code: 'ö', Text: "ö"},
		"ctrl+c":       {Code: 'c', Mod: tea.ModCtrl},
		"ctrl+C":       {Code: 'c', Mod: tea.ModCtrl},
		"Ctrl+Shift+a": {Code: 'a', Mod: tea.ModCtrl | tea.ModShift},
		"shift+tab":    {Code: tea.KeyTab, Mod: tea.ModShift},
		"alt+enter":    {Code: tea.KeyEnter, Mod: tea.ModAlt},
		"alt+g":        {Code: 'g', Mod: tea.ModAlt},
		"alt+G":        {Code: 'g', Mod: tea.ModAlt | tea.ModShift},
		"ctrl++":       {Code: '+', Mod: tea.ModCtrl},
		"alt++":        {Code: '+', Mod: tea.ModAlt},
		" super+a ":    {Code: 'a', Mod: tea.ModSuper},
	} {
		t.Run(spec, func(t *testing.T) {
			t.Parallel()

			got, err := tuikit.ParseKey(spec)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}

	for spec, want := range map[string]string{
		"":        "empty key",
		"entr":    `unknown key: "entr"`,
		"f1":      `unknown key: "f1"`,
		"ctrl+":   `unknown key: "ctrl+"`,
		"ctrl+ab": `unknown key: "ab"`,
		"crtl+c":  `unknown modifier: "crtl"`,
		"+c":      `unknown modifier: ""`,
	} {
		t.Run("not a key: "+spec, func(t *testing.T) {
			t.Parallel()

			_, err := tuikit.ParseKey(spec)
			require.EqualError(t, err, want)
		})
	}
}
