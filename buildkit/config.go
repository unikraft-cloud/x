// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package buildkit

import (
	"encoding/csv"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Format is a buildkit client that Config.Print writes commands for.
type Format string

const (
	// FormatBuildctl is a buildctl command line.
	FormatBuildctl Format = "buildctl"
	// FormatBuildx is a docker buildx create command for the remote driver.
	FormatBuildx Format = "buildx"
	// FormatEnv is a BUILDKIT_HOST export, then the buildctl flags for mTLS.
	FormatEnv Format = "env"
)

// Config is what a buildkit client needs to reach a builder.
type Config struct {
	// Name is the name of the buildx builder.
	Name string
	// Addr is the builder's buildkit address, such as unix:///run/buildkit.sock.
	Addr string
	// ServerName is the name in the builder's certificate, Dir holds the files
	// of TLS.WriteDir. Both are empty without mTLS.
	ServerName, Dir string
}

// Print writes the shell commands that point the client of f at the builder.
func (c Config) Print(w io.Writer, f Format) error {
	var lines [][]string
	switch f {
	case FormatBuildctl:
		args := []string{"buildctl", "--addr", c.Addr}
		if c.Dir != "" {
			args = append(args,
				"--tlsservername", c.ServerName,
				"--tlscacert", filepath.Join(c.Dir, caFile),
				"--tlscert", filepath.Join(c.Dir, certFile),
				"--tlskey", filepath.Join(c.Dir, keyFile),
			)
		}
		lines = [][]string{args}
	case FormatBuildx:
		args := []string{"docker", "buildx", "create"}
		if c.Name != "" {
			args = append(args, "--name", c.Name)
		}
		args = append(args, "--driver", "remote")
		if c.Dir != "" {
			// Buildx reads the driver options as one CSV record.
			var opts strings.Builder
			w := csv.NewWriter(&opts)
			_ = w.Write([]string{
				"cacert=" + filepath.Join(c.Dir, caFile),
				"cert=" + filepath.Join(c.Dir, certFile),
				"key=" + filepath.Join(c.Dir, keyFile),
				"servername=" + c.ServerName,
			})
			w.Flush()
			args = append(args, "--driver-opt", strings.TrimSuffix(opts.String(), "\n"))
		}
		lines = [][]string{append(args, c.Addr)}
	case FormatEnv:
		lines = [][]string{{"export", "BUILDKIT_HOST=" + c.Addr}}
		if c.Dir != "" {
			lines = append(lines, []string{"buildctl", "--tlsdir", c.Dir, "--tlsservername", c.ServerName})
		}
	default:
		return fmt.Errorf("unknown format %q", f)
	}

	for _, args := range lines {
		for i, a := range args {
			args[i] = quote(a)
		}
		if _, err := fmt.Fprintln(w, strings.Join(args, " ")); err != nil {
			return err
		}
	}
	return nil
}

// shellSafe holds the characters that a POSIX shell reads as they are.
const shellSafe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-"

// quote puts s in single quotes if it has characters that are not shellSafe.
func quote(s string) string {
	if s != "" && strings.Trim(s, shellSafe) == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
