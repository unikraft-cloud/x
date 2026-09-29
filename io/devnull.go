// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package io

import stdio "io"

// DevNull reads nothing, swallows everything and closes for free.
var DevNull stdio.ReadWriteCloser = devNull{}

type devNull struct{}

func (devNull) Read([]byte) (int, error)    { return 0, stdio.EOF }
func (devNull) Write(p []byte) (int, error) { return len(p), nil }
func (devNull) Close() error                { return nil }
