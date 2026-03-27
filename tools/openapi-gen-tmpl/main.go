// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"

	"unikraft.com/x/kingkong"
	"unikraft.com/x/log"
)

type OpenApiGenTmplCli struct {
	// Hidden configuration
	Context context.Context `kong:"-"`
	Stderr  io.Writer       `kong:"-"`
	Stdout  io.Writer       `kong:"-"`

	// Logging configuration
	LogLevel log.Level `help:"Set the logging level." enum:"trace,debug,info,warn,error,fatal,panic" default:"info"`
	LogType  log.Type  `help:"Set the logging type." enum:"json,text" default:"text"`

	// User configuration
	Templates   string `short:"t" placeholder:"DIR" help:"Path to templates directory." required:"" type:"existingdir"`
	OpenApiSpec []byte `short:"i" placeholder:"SPEC" help:"Path to OpenAPI spec file (or - for stdin)." required:"" type:"filecontent"`
	Output      string `short:"o" placeholder:"DIR" help:"Output directory for generated files." required:""`
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var (
		err error

		args   = os.Args[1:]
		stdin  = os.Stdin
		stdout = os.Stdout
		stderr = os.Stderr
	)

	ctx, err = exec(ctx, args, stdin, stdout, stderr)
	if err == nil {
		// catch context cancellation errors, and make sure we show them, even if
		// the command succeeded
		err = ctx.Err()
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		log.G(ctx).Error().Msg(err.Error())
	}
	if err != nil {
		os.Exit(1)
	}
}

func exec(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (context.Context, error) {
	var cli OpenApiGenTmplCli
	kctx := kong.Parse(&cli,
		kong.Name("openapi-gen-tmpl"),
		kong.Description("Generate SDKs via templates from OpenAPI spec"),
		kong.Help(kingkong.HelpPrinter("")),
	)

	cli.Context = ctx

	var level log.Level
	switch cli.LogLevel.String() {
	case "trace":
		level = log.TraceLevel
	case "debug":
		level = log.DebugLevel
	case "info":
		level = log.InfoLevel
	case "warn":
		level = log.WarnLevel
	case "error":
		level = log.ErrorLevel
	case "fatal":
		level = log.FatalLevel
	case "panic":
		level = log.PanicLevel
	default:
		level = log.InfoLevel
	}

	cli.Context = log.WithLogger(cli.Context, log.New(stderr, cli.LogType, level))
	kctx.BindTo(cli.Context, (*context.Context)(nil))

	return cli.Context, kctx.Run()
}

func (cli *OpenApiGenTmplCli) Run() error {
	ctx := cli.Context

	log.G(ctx).
		Info().
		Msg("starting OpenAPI template generator")

	generator, err := NewGenerator(
		cli.Templates,
		cli.OpenApiSpec,
		cli.Output,
	)
	if err != nil {
		return fmt.Errorf("error creating generator: %w", err)
	}

	if err := os.MkdirAll(cli.Output, 0o755); err != nil {
		return fmt.Errorf("error creating output directory: %w", err)
	}

	files := generator.Files(ctx)
	if len(files) == 0 {
		return fmt.Errorf("no template files found to generate")
	}

	for _, file := range files {
		log.G(ctx).
			Info().
			Str("file", file.Basename).
			Msg("generating file from template")
		if err := file.Generate(cli.Output); err != nil {
			return fmt.Errorf("error generating %s: %w", file.Basename, err)
		}
	}

	log.G(ctx).
		Info().
		Msg("code generation completed successfully")

	return nil
}
