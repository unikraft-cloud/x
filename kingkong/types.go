// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package kingkong

type AdditionalHelp interface {
	HelpSections() []HelpSection
}

type HelpSection struct {
	// Title is the title of the help section.
	Title string
	// Content contains the help section content.
	Content string
}
