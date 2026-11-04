// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !js

package shell

import (
	"context"
	"errors"
	"io"
	"os/exec"

	"github.com/creack/pty"
)

// LocalTransport stands in for an instance by running commands on this
// machine, so the routing, the remote filesystem handlers and the prompt can
// all be exercised without a network.
func LocalTransport() ExecTransport { return runLocal }

func runLocal(ctx context.Context, cmd Command) (int, error) {
	proc := exec.CommandContext(ctx, cmd.Args[0], cmd.Args[1:]...)
	proc.Dir = cmd.Dir
	proc.Env = cmd.Env

	// Before the process group and the pipes: a pty gives the command a session of its own.
	if cmd.Terminal != nil {
		ptmx, err := pty.StartWithSize(proc, winsize(cmd.Terminal.Size))
		switch {
		case err == nil:
			defer ptmx.Close()
			ctx, stop := context.WithCancel(ctx)
			defer stop()

			if cmd.Streams.Stdin != nil {
				go func() { _, _ = io.Copy(ptmx, cmd.Streams.Stdin) }()
			}
			go func() {
				for {
					select {
					case size := <-cmd.Terminal.Resize:
						_ = pty.Setsize(ptmx, winsize(size))
					case <-ctx.Done():
						return
					}
				}
			}()
			// The read ends with EIO once the command has hung up.
			_, _ = io.Copy(cmd.Streams.Stdout, ptmx)
			return ExitStatus(proc.Wait())
		case !errors.Is(err, pty.ErrUnsupported):
			return 0, err
		}
	}

	// Its own process group, as a command on an instance is: the terminal's own
	// ^C reaches the shell, never the command.
	proc.SysProcAttr = ownProcessGroup()
	proc.Stdout, proc.Stderr = cmd.Streams.Stdout, cmd.Streams.Stderr

	// Stdin goes through a pipe the command's exit closes, as [Transport] asks:
	// handed the reader itself, Wait would hold out for its EOF, which for the
	// terminal only comes once the shell has this command's exit in hand.
	if cmd.Streams.Stdin != nil {
		stdin, err := proc.StdinPipe()
		if err != nil {
			return 0, err
		}
		defer stdin.Close()
		go func() {
			_, _ = io.Copy(stdin, cmd.Streams.Stdin)
			_ = stdin.Close()
		}()
	}

	return ExitStatus(proc.Run())
}

func winsize(size WinSize) *pty.Winsize {
	return &pty.Winsize{Cols: uint16(size.Cols), Rows: uint16(size.Rows)}
}
