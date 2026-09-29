// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os/exec"
	"strings"
	"syscall"

	"unikraft.com/x/stdio"
)

// Command is one command a [Transport] runs, shaped like the [os/exec.Cmd] a
// caller already holds so that one can be filled in field for field. Env is
// [os/exec.Cmd]'s too: NAME=value entries, and none of them means the
// environment the instance would have given the command itself.
type Command struct {
	Args    []string
	Dir     string
	Env     []string
	Streams stdio.Stdio
}

// Transport is how the shell reaches the instance.
type Transport interface {
	// Exec runs one command, its status negated when it was signalled
	Exec(ctx context.Context, cmd Command) (int, error)

	// Stat is what is at a path, the symlink itself when not following
	Stat(ctx context.Context, dir, name string, followSymlinks bool) (fs.FileInfo, error)

	// Access is whether the file can be used every way mode asks
	Access(ctx context.Context, dir, name string, mode AccessMode) error

	// ReadDir lists a directory, by name
	ReadDir(ctx context.Context, dir, name string) ([]fs.DirEntry, error)

	// Open streams a file one way, telling stderr what goes wrong after
	Open(ctx context.Context, dir, name string, flag int, stderr io.Writer) (io.ReadWriteCloser, error)

	// Environ is the instance's own environment, as NAME=value
	Environ(ctx context.Context) ([]string, error)
}

// AccessMode is what a caller wants to do with a file.
type AccessMode uint8

const (
	AccessRead AccessMode = 1 << iota
	AccessWrite
	AccessExec
)

// ExitStatus is the status a [Transport] reports for the command it ran: an
// exit is a status, and anything else is the transport itself failing. A
// command a signal ended is its number negated, as [Transport] asks.
func ExitStatus(err error) (int, error) {
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exitErr):
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return -int(status.Signal()), nil
		}
		return exitErr.ExitCode(), nil
	}

	coded, ok := errors.AsType[exitCoder](err)
	switch {
	case !ok:
		return 0, err
	case coded.ExitCode() < 0:
		// -1 is a signal the error did not name, so the shell reports the one a
		// prompt sees: the command was cut short, not that it exited.
		return StatusInterrupted, nil
	default:
		return coded.ExitCode(), nil
	}
}

// exitCoder is what a command that ran and ended answers with; ExitCode is
// [exec.ExitError.ExitCode], -1 included.
type exitCoder interface {
	error
	ExitCode() int
}

// sanitised is a transport's error to show a person, with the URL and the
// network address of whatever it reached taken out of the text.
func sanitised(err error) error {
	if err == nil {
		return nil
	}

	said := err.Error()
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		switch e := cause.(type) {
		case *url.Error:
			if e.Err != nil {
				said = strings.Replace(said, e.Error(), e.Op+": "+e.Err.Error(), 1)
			}
		case *net.OpError:
			if e.Err != nil {
				said = strings.Replace(said, e.Error(), strings.TrimSpace(e.Op+" "+e.Net)+": "+e.Err.Error(), 1)
			}
		}
	}

	if said == err.Error() {
		return err
	}
	return sanitisedError{error: err, said: said}
}

// sanitisedError is the error it was made from, and only its text differs.
type sanitisedError struct {
	error
	said string
}

func (e sanitisedError) Error() string { return e.said }

func (e sanitisedError) Unwrap() error { return e.error }
