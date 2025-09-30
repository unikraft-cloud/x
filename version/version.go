// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package version

import (
	"runtime"

	"github.com/MakeNowJust/heredoc"
)

var (
	Tool      = "unset"
	Docs      = "unset"
	Issues    = "unset"
	Version   = "unset"
	Commit    = "unset"
	BuildTime = "unset"
)

// String returns a one-line string with the version information.
func String() string {
	return Tool + " " + Version + " (" + Commit + ")" + BuildTime
}

// Map returns a map of version information.
func Map() map[string]string {
	return map[string]string{
		"tool":       Tool,
		"docs":       Docs,
		"issues":     Issues,
		"version":    Version,
		"commit":     Commit,
		"build_time": BuildTime,
		"go_version": runtime.Version(),
		"platform":   runtime.GOOS + "/" + runtime.GOARCH,
	}
}

// Long returns a long, multi-line string with the version information.
func Long() string {
	return heredoc.Docf(`
		%s
		  version   : %s
		  commit    : %s
		  platform  : %s/%s
		  build time: %s
		  go version: %s
		  docs      : %s
		  issues    : %s`,
		Tool,
		Version,
		Commit,
		runtime.GOOS,
		runtime.GOARCH,
		BuildTime,
		runtime.Version(),
		Docs,
		Issues,
	)
}

// UserAgent returns the user agent string for the Unikraft CLI.
func UserAgent() string {
	return heredoc.Docf(
		"%s/%s (%s) %s/%s",
		Tool,
		Version,
		Commit,
		runtime.GOOS,
		runtime.GOARCH,
	)
}
