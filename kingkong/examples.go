// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package kingkong

// Example is a type alias for a set of two strings: one for the description
// of the example and the second being the code snippet.
type Example struct {
	Description string
	Commands    []string
}

// ExamplesProvider is an interface that defines a method to return examples for
// a command.  It is used to provide an array of  examples for commands in the
// CLI.  The method returns a slice of examples, where each example is a tuple
// containing a description (which will be prefixed with `#`) and the command
// itself.
type ExamplesProvider interface {
	Examples() []Example
}
