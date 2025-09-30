// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package startstopper defines the interface for services that can be started
// and stopped.
package startstopper

import "context"

// StartStopper defines an interface for services that can be started and
// stopped.
type StartStopper interface {
	// Start starts the service.
	Start(context.Context) error

	// Stop stops the service.
	Stop(context.Context) error
}
