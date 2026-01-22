// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package version

import (
	"cmp"
	"runtime"
	"runtime/debug"

	"github.com/MakeNowJust/heredoc"
)

var (
	Tool      = "unikraft"
	Docs      = ""
	Issues    = ""
	Version   = "v0.0.0"
	Commit    = ""
	BuildTime = ""
)

func init() {
	if Commit == "" || BuildTime == "" {
		info, ok := debug.ReadBuildInfo()
		if ok {
			for _, setting := range info.Settings {
				switch setting.Key {
				case "vcs.revision":
					Commit = cmp.Or(Commit, setting.Value)
				case "vcs.time":
					BuildTime = cmp.Or(BuildTime, setting.Value)
				}
			}
		}
	}
}

// String returns a one-line string with the version information.
func String() string {
	return Tool + " " + Version + " (" + Commit + ")" + BuildTime
}

// Map returns a map of version information.
func Map() map[string]string {
	return map[string]string{
		"tool":       Tool,
		"docs":       wrapEmpty(Docs),
		"issues":     wrapEmpty(Issues),
		"version":    Version,
		"commit":     wrapEmpty(Commit),
		"build_time": wrapEmpty(BuildTime),
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
		wrapEmpty(Commit),
		runtime.GOOS,
		runtime.GOARCH,
		wrapEmpty(BuildTime),
		runtime.Version(),
		wrapEmpty(Docs),
		wrapEmpty(Issues),
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

func wrapEmpty(value string) string {
	return cmp.Or(value, "unset")
}
