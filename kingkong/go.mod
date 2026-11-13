module unikraft.com/x/kingkong

go 1.27.3

require (
	charm.land/lipgloss/v2 v2.0.6
	github.com/alecthomas/kong v1.17.0
	github.com/charmbracelet/x/ansi v0.11.9
	github.com/stretchr/testify v1.12.1
	gotest.tools/v3 v3.5.2
	unikraft.com/x/colors v0.0.0-20260813113709-544c471e0bc9
	unikraft.com/x/guesstermwidth v0.0.0-20260813113709-544c471e0bc9
)

require (
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20260812204455-68fa937c71be // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/mattn/go-runewidth v0.0.31 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/xo/terminfo v1.0.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/exp v0.0.0-20260908205506-85c1c2202aba // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
)

// Use the fork with charmbracelet/lipgloss#746 until a release has it.
replace charm.land/lipgloss/v2 => github.com/unikraft-cloud/lipgloss/v2 v2.0.0-20260921145830-9a285e5dd507
