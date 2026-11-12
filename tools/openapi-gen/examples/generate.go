// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package examples holds a worked example of tools/openapi-gen's go-client
// and go-server templates: api.yaml is the input spec, and client/ and
// server/ are its committed generated output. namespaced/ holds a shared
// common/v1 package that machines/v1 imports with --namespace-import-path.
// Run `go generate ./...` after changing a spec or the templates to refresh
// them.
package examples

//go:generate go run unikraft.com/x/tools/openapi-gen -i api.yaml -o client -t ../templates/go-client -v package=client
//go:generate go run unikraft.com/x/tools/openapi-gen -i api.yaml -o server -t ../templates/go-server -v package=server
//go:generate go run unikraft.com/x/tools/openapi-gen -i namespaced/api.yaml -o namespaced/common/v1 -t ../templates/go-server -v package=commonv1 --models-only --namespace=Org --namespace-flatten=strip
//go:generate go run unikraft.com/x/tools/openapi-gen -i namespaced/api.yaml -o namespaced/machines/v1 -t ../templates/go-server -v package=machinesv1 --tag=machines --namespace=Machines --namespace-import-path=Org.Common=unikraft.com/x/tools/openapi-gen/examples/namespaced/common/v1 --namespace-flatten=strip
