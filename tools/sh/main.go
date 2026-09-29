// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Command sh runs x/shell against this machine, so the prompt, the routing and
// the remote filesystem handlers can be driven by hand without an instance.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"syscall"

	"unikraft.com/x/shell"
	xsignal "unikraft.com/x/signal"
	"unikraft.com/x/stdio"
)

func main() {
	command := flag.String("c", "", "run a command line instead of prompting")
	flag.Parse()

	ctx, signals := xsignal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer signals.Stop()

	dir, err := os.Getwd()
	if err != nil {
		dir = "/"
	}

	status, err := shell.Run(ctx, shell.Config{
		Instance:       "local",
		Transport:      shell.LocalTransport(),
		Dir:            dir,
		Command:        *command,
		Banner:         "x/shell against this machine; :help lists the builtins.",
		SuspendSignals: signals.Suspend,
	}, stdio.Stdio{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr})
	if err != nil {
		fmt.Fprintln(os.Stderr, "sh:", err)
		os.Exit(1)
	}
	os.Exit(status)
}
