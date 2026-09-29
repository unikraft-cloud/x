// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package kingkong

import (
	"bytes"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"gotest.tools/v3/golden"
)

type helpGoldenCLI struct {
	Config  string `help:"Path to config file." default:"./kingkong.yaml" env:"KINGKONG_CONFIG" group:"core"`
	Profile string `help:"Runtime profile." enum:"dev,staging,prod" default:"dev" group:"core"`
	Output  string `help:"Output format." enum:"json,yaml,text" default:"json" group:"core"`
	Value   string `help:"Value to apply." placeholder:"<value>" group:"core"`
	Filter  string `help:"Filter in key-value form." placeholder:"<key>=<value>" group:"core"`
	Verbose bool   `help:"Enable verbose logging." short:"v" group:"core"`
	Debug   bool   `help:"Enable debug logging." negatable:"" env:"KINGKONG_DEBUG" group:"core"`

	Host string `help:"Bind address." default:"127.0.0.1" group:"network"`
	Port int    `help:"Bind port." default:"8080" group:"network"`

	Init  helpGoldenInitCmd  `cmd:"" help:"Initialize a project." group:"lifecycle"`
	Serve helpGoldenServeCmd `cmd:"" aliases:"run,server" help:"Run the service." group:"lifecycle"`
}

func (helpGoldenCLI) Examples() []Example {
	return []Example{
		{
			Description: "Initialize a new project.",
			Commands: []string{
				"kingkong init --template=go",
				"kingkong init --template=rust --force",
			},
		},
		{
			Description: "Run with a custom config.",
			Commands: []string{
				"kingkong serve ./configs/dev.yaml",
				"kingkong serve ./configs/prod.yaml prod",
			},
		},
	}
}

func (helpGoldenCLI) HelpSections() []HelpSection {
	return []HelpSection{
		{
			Title:   "Environment",
			Content: "Use KINGKONG_CONFIG to override the config path.",
		},
	}
}

type helpGoldenInitCmd struct {
	Template string `help:"Project template." enum:"go,rust" default:"go"`
	Force    bool   `help:"Overwrite existing files." short:"f"`
}

type helpGoldenServeCmd struct {
	Config  string `arg:"" help:"Path to service config."`
	Profile string `arg:"" optional:"" help:"Runtime profile." enum:"dev,staging,prod" default:"dev"`
	Port    int    `name:"listen-port" help:"Port to listen on." default:"8080"`
	TLS     bool   `help:"Enable TLS." negatable:""`
}

type exitCode struct {
	code int
}

func (e exitCode) Error() string {
	return fmt.Sprintf("exit %d", e.code)
}

func TestHelpGolden(t *testing.T) {
	t.Setenv("COLUMNS", "120")

	var cli helpGoldenCLI
	buf := &bytes.Buffer{}

	app, err := kong.New(
		&cli,
		kong.Name("kingkong"),
		kong.Description("Kingkong is a CLI showcase for rich help output."),
		kong.Help(HelpPrinter("v0.0.0-test")),
		kong.HelpOptions{
			ValueFormatter: helpValueFormatter,
			WrapUpperBound: 120,
		},
		kong.Groups{
			"core":      "Core Flags\nCommon settings for every command.",
			"network":   "Network Flags\nConnection-related options.",
			"lifecycle": "Lifecycle Commands\nProject setup and runtime operations.",
		},
		kong.Writers(buf, buf),
		kong.Exit(func(code int) {
			panic(exitCode{code: code})
		}),
	)
	require.NoError(t, err)

	output := captureHelpOutput(t, app, buf)
	normalized := normalizeHelpOutput(output)

	golden.Assert(t, normalized, "help.golden")
}

func TestHelpDescriptionDetailOverrides(t *testing.T) {
	t.Setenv("COLUMNS", "120")

	var cli struct{}
	buf := &bytes.Buffer{}

	app, err := kong.New(
		&cli,
		kong.Name("kingkong"),
		kong.Description("Short description that should be replaced."),
		DescriptionDetail("Detailed description appears at the top."),
		kong.Help(HelpPrinter("v0.0.0-test")),
		kong.HelpOptions{
			ValueFormatter: helpValueFormatter,
			WrapUpperBound: 120,
		},
		kong.Writers(buf, buf),
		kong.Exit(func(code int) {
			panic(exitCode{code: code})
		}),
	)
	require.NoError(t, err)

	output := captureHelpOutput(t, app, buf)
	normalized := normalizeHelpOutput(output)

	golden.Assert(t, normalized, "help-detail.golden")
}

func TestCollapsedFlagPlaceholders(t *testing.T) {
	type collapseCLI struct {
		Foo string `help:"Foo value." placeholder:"<value>" collapse:"pair"`
		Bar string `help:"Bar filter." placeholder:"<key>=<value>" collapse:"pair" aliases:"baz"`
	}

	var cli collapseCLI
	app, err := kong.New(&cli)
	require.NoError(t, err)

	flags := collapseFlags(app.Model.Flags)
	var collapsed *Flag
	for _, flag := range flags {
		if flag.Name == "foo" {
			collapsed = flag
			break
		}
	}
	require.NotNil(t, collapsed)

	formatted := strings.TrimSpace(ansi.Strip(formatFlag(collapsed)))
	require.Equal(t, "--foo=<value>, --bar=<key>=<value>, --baz", formatted)
}

func TestRepeatableFlagPlaceholders(t *testing.T) {
	type repeatableCLI struct {
		Tag []string          `help:"Tag." placeholder:"<tag>" sep:"none"`
		Set map[string]string `help:"Set." placeholder:"<key>=<value>" mapsep:"none"`
		CSV []string          `help:"CSV-joinable list." placeholder:"<value>"`
		Str string            `help:"Scalar value." placeholder:"<value>"`
	}

	var cli repeatableCLI
	app, err := kong.New(&cli)
	require.NoError(t, err)

	flagByName := func(name string) *kong.Flag {
		for _, flag := range app.Model.Flags {
			if flag.Name == name {
				return flag
			}
		}
		require.Failf(t, "flag not found", "no flag named %q", name)
		return nil
	}

	// A slice flag gets the "..." suffix whether or not it also splits on a
	// separator: either way, repeating the flag accumulates more values.
	formatted := ansi.Strip(formatFlag(&Flag{Flag: flagByName("tag")}))
	require.Contains(t, formatted, "--tag=<tag> ...")

	formatted = ansi.Strip(formatFlag(&Flag{Flag: flagByName("set")}))
	require.Contains(t, formatted, "--set=<key>=<value> ...")

	formatted = ansi.Strip(formatFlag(&Flag{Flag: flagByName("csv")}))
	require.Contains(t, formatted, "--csv=<value> ...")

	// A scalar flag never gets the suffix.
	formatted = ansi.Strip(formatFlag(&Flag{Flag: flagByName("str")}))
	require.NotContains(t, formatted, "...")
}

func TestCollapsedRepeatableFlagPlaceholders(t *testing.T) {
	type collapseCLI struct {
		Foo []string `help:"Foo values." placeholder:"<value>" collapse:"pair" sep:"none"`
		Bar string   `help:"Bar filter." placeholder:"<key>=<value>" collapse:"pair"`
	}

	var cli collapseCLI
	app, err := kong.New(&cli)
	require.NoError(t, err)

	flags := collapseFlags(app.Model.Flags)
	var collapsed *Flag
	for _, flag := range flags {
		if flag.Name == "foo" {
			collapsed = flag
			break
		}
	}
	require.NotNil(t, collapsed)

	formatted := strings.TrimSpace(ansi.Strip(formatFlag(collapsed)))
	require.Equal(t, "--foo=<value> ..., --bar=<key>=<value>", formatted)
}

func captureHelpOutput(t *testing.T, app *kong.Kong, buf *bytes.Buffer) (output string) {
	t.Helper()

	defer func() {
		recovered := recover()
		require.NotNil(t, recovered, "expected help to exit")
		_, ok := recovered.(exitCode)
		require.True(t, ok, "unexpected panic: %v", recovered)
		output = buf.String()
	}()

	_, err := app.Parse([]string{"--help"})
	require.NoError(t, err)

	return ""
}

func normalizeHelpOutput(output string) string {
	// Strip ANSI escape codes to ensure consistent comparison across
	// different terminal environments. This is necessary because lipgloss v2
	// initializes its global Writer at startup, before tests can set NO_COLOR.
	output = ansi.Strip(output)
	output = strings.ReplaceAll(output, runtime.GOOS, "{{GOOS}}")
	output = strings.ReplaceAll(output, runtime.GOARCH, "{{GOARCH}}")
	return output
}

// An enum whose members include an empty string is how kong spells "may be
// left unset", and it needs an empty default to go with it. Neither is a
// choice a user can type, so neither belongs in the rendered list.
func TestOptionalEnumChoices(t *testing.T) {
	type optionalEnumCLI struct {
		Segment string `help:"Private supernet." default:"" enum:",10.0.0.0/8,172.16.0.0/12"`
		Mode    string `help:"Mode." default:"isolates" enum:"isolates,other"`
	}

	var cli optionalEnumCLI
	app, err := kong.New(&cli)
	require.NoError(t, err)

	flagByName := func(name string) *kong.Flag {
		for _, flag := range app.Model.Flags {
			if flag.Name == name {
				return flag
			}
		}
		require.Failf(t, "flag not found", "no flag named %q", name)
		return nil
	}

	formatted := ansi.Strip(helpValueFormatter(flagByName("segment").Value))
	require.Contains(t, formatted, "[choices: 10.0.0.0/8, 172.16.0.0/12]")

	// The bracket used to open only for a non-empty default, so an optional
	// enum rendered a dangling "]" with no "[".
	require.NotContains(t, formatted, "[default:")

	formatted = ansi.Strip(helpValueFormatter(flagByName("mode").Value))
	require.Contains(t, formatted, "[default: isolates, choices: isolates, other]")
}
