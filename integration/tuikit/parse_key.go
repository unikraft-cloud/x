// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package tuikit

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

// ParseKey parses a key specification into a Bubble Tea key event.
//
// Bubble Tea/Ultraviolet don't currently expose a public string -> tea.Key
// parser, so we keep a small one for tests/tools.
//
// Supported forms:
//
//   - Named keys: "enter", "tab", "esc", "up", "down", ...
//   - Single runes: "r", "?", "G", ...
//   - Keystrokes with modifiers: "ctrl+c", "shift+tab", "alt+enter", ...
func ParseKey(spec string) (tea.Key, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return tea.Key{}, errors.New("empty key")
	}

	if strings.Contains(spec, "+") {
		return parseKeystroke(spec)
	}

	lower := strings.ToLower(spec)
	if code, ok := keyNameToCode[lower]; ok {
		return tea.Key{Code: code}, nil
	}

	if utf8.RuneCountInString(spec) == 1 {
		r, _ := utf8.DecodeRuneInString(spec)
		return tea.Key{Code: r, Text: spec}, nil
	}

	// Multi-rune input (e.g. paste).
	return tea.Key{Code: tea.KeyExtended, Text: spec}, nil
}

var keyNameToCode = map[string]rune{
	"enter":     tea.KeyEnter,
	"return":    tea.KeyEnter,
	"tab":       tea.KeyTab,
	"backspace": tea.KeyBackspace,
	"esc":       tea.KeyEscape,
	"escape":    tea.KeyEscape,
	"space":     tea.KeySpace,
	"up":        tea.KeyUp,
	"down":      tea.KeyDown,
	"left":      tea.KeyLeft,
	"right":     tea.KeyRight,
	"pgup":      tea.KeyPgUp,
	"pgdown":    tea.KeyPgDown,
	"home":      tea.KeyHome,
	"end":       tea.KeyEnd,
	"insert":    tea.KeyInsert,
	"delete":    tea.KeyDelete,
}

func parseKeystroke(spec string) (tea.Key, error) {
	parts := strings.Split(spec, "+")
	if len(parts) == 0 {
		return tea.Key{}, fmt.Errorf("invalid keystroke: %q", spec)
	}

	var (
		mod  tea.KeyMod
		code rune
		text string
		saw  bool
	)

	for _, raw := range parts {
		part := strings.ToLower(strings.TrimSpace(raw))
		if part == "" {
			return tea.Key{}, fmt.Errorf("invalid keystroke: %q", spec)
		}
		switch part {
		case "ctrl", "control":
			mod |= tea.ModCtrl
			continue
		case "alt", "option":
			mod |= tea.ModAlt
			continue
		case "shift":
			mod |= tea.ModShift
			continue
		case "meta", "cmd", "command":
			mod |= tea.ModMeta
			continue
		case "super":
			mod |= tea.ModSuper
			continue
		case "hyper":
			mod |= tea.ModHyper
			continue
		}

		if c, ok := keyNameToCode[part]; ok {
			code = c
			saw = true
			continue
		}

		if utf8.RuneCountInString(part) == 1 {
			r, _ := utf8.DecodeRuneInString(part)
			// Normalize ctrl+<letter> to lowercase.
			if mod.Contains(tea.ModCtrl) && r >= 'A' && r <= 'Z' {
				r = r + ('a' - 'A')
			}
			code = r
			saw = true
			continue
		}

		code = tea.KeyExtended
		text = raw
		saw = true
	}

	if !saw {
		return tea.Key{}, fmt.Errorf("invalid keystroke: %q", spec)
	}

	k := tea.Key{Code: code, Mod: mod}
	if text != "" {
		k.Text = text
	}
	return k, nil
}
