// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import _ "embed"

// The sh the shell runs on the instance to stand in for a filesystem and an
// environment it cannot reach directly. Each file documents the protocol its
// Go caller parses; all of them travel as `sh -c <script> sh <args...>`.
var (
	//go:embed scripts/stat.sh
	statScript string

	//go:embed scripts/access.sh
	accessScript string

	//go:embed scripts/readdir.sh
	readDirScript string

	//go:embed scripts/read.sh
	readScript string

	//go:embed scripts/write.sh
	writeScript string

	//go:embed scripts/append.sh
	appendScript string

	//go:embed scripts/environ.sh
	environProbe string

	//go:embed scripts/commands.sh
	commandsScript string
)
