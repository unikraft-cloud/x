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
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"
	"gotest.tools/v3/golden"
)

type helpGoldenCLI struct {
	Config  string `help:"Path to config file." default:"./kingkong.yaml" env:"KINGKONG_CONFIG" placeholder:"FILE" group:"core"`
	Profile string `help:"Runtime profile." enum:"dev,staging,prod" default:"dev" group:"core"`
	Output  string `help:"Output format." enum:"json,yaml,text" default:"json" group:"core"`
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
	lipgloss.SetColorProfile(termenv.Ascii)

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
	output = strings.ReplaceAll(output, runtime.GOOS, "{{GOOS}}")
	output = strings.ReplaceAll(output, runtime.GOARCH, "{{GOARCH}}")
	return output
}
