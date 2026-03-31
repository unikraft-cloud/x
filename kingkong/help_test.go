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
