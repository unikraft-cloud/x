module unikraft.com/x/colors

go 1.27.3

require charm.land/lipgloss/v2 v2.0.6

require (
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20260812204455-68fa937c71be // indirect
	github.com/charmbracelet/x/ansi v0.11.9 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/mattn/go-runewidth v0.0.27 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/xo/terminfo v1.0.0 // indirect
	golang.org/x/exp v0.0.0-20260908205506-85c1c2202aba // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

// Use the fork with charmbracelet/lipgloss#746 until a release has it.
replace charm.land/lipgloss/v2 => github.com/unikraft-cloud/lipgloss/v2 v2.0.0-20260921145830-9a285e5dd507
