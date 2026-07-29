// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package examples holds a worked example of tools/openapi-gen's go-client
// and go-server templates: api.yaml is the input spec, and client/ and
// server/ are its committed generated output. Run `go generate ./...` after
// changing api.yaml or the templates to refresh them.
package examples

//go:generate go run unikraft.com/x/tools/openapi-gen -i api.yaml -o client -t ../templates/go-client -v package=client
//go:generate go run unikraft.com/x/tools/openapi-gen -i api.yaml -o server -t ../templates/go-server -v package=server
