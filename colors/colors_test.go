// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package colors

import (
	"image/color"
	"testing"
)

func TestAdaptiveColor(t *testing.T) {
	c := AdaptiveColor{Light: Blue100, Dark: Blue900}
	prev := HasDarkBackground
	t.Cleanup(func() { HasDarkBackground = prev })

	for _, dark := range []bool{true, false} {
		HasDarkBackground = dark
		want := color.Color(Blue100)
		if dark {
			want = Blue900
		}
		if color.RGBAModel.Convert(c) != color.RGBAModel.Convert(want) {
			t.Errorf("dark background %v: got %v, want %v", dark, color.RGBAModel.Convert(c), want)
		}
	}
}
