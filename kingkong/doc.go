// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package kingkong provides a styled help-rendering layer on top of the
// alecthomas/kong CLI framework. Kong's built-in help printer produces plain
// text; this package replaces it with a richly formatted, colorized,
// terminal-width-aware help printer.
//
// The package supports flag grouping and collapsing, command grouping, usage
// examples via the ExamplesProvider interface, and additional help sections via
// the AdditionalHelp interface. Styling is handled through lipgloss, producing
// ANSI-colored output for commands, flags, placeholders, and environment
// variables.
package kingkong
