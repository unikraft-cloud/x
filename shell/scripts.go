// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import _ "embed"

// HACK: the sandbox API runs commands and can mkdir, read and write a whole
// file, but cannot stat, list a directory, test access, append or stream into a
// file.
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
