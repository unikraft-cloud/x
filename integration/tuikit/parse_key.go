// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package tuikit

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
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

	// The key is what follows the last +, which is itself a + in "ctrl++".
	mods, name, modified := "", spec, false
	if i := strings.LastIndex(spec[:len(spec)-1], "+"); i >= 0 {
		mods, name, modified = spec[:i], spec[i+1:], true
	}

	var key tea.Key
	if code, ok := keyNameToCode[strings.ToLower(name)]; ok {
		key.Code = code
	} else if r, size := utf8.DecodeRuneInString(name); size == len(name) {
		key.Code, key.Text = r, name
	} else {
		return tea.Key{}, fmt.Errorf("unknown key: %q", name)
	}
	if !modified {
		return key, nil
	}

	key.Text = ""
	for mod := range strings.SplitSeq(mods, "+") {
		switch strings.ToLower(strings.TrimSpace(mod)) {
		case "ctrl", "control":
			key.Mod |= tea.ModCtrl
		case "alt", "option":
			key.Mod |= tea.ModAlt
		case "shift":
			key.Mod |= tea.ModShift
		case "meta", "cmd", "command":
			key.Mod |= tea.ModMeta
		case "super":
			key.Mod |= tea.ModSuper
		case "hyper":
			key.Mod |= tea.ModHyper
		default:
			return tea.Key{}, fmt.Errorf("unknown modifier: %q", mod)
		}
	}
	// A capital with a modifier is the shift of its letter, but for ctrl,
	// which has no capitals.
	if unicode.IsUpper(key.Code) {
		key.Code = unicode.ToLower(key.Code)
		if !key.Mod.Contains(tea.ModCtrl) {
			key.Mod |= tea.ModShift
		}
	}

	return key, nil
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
