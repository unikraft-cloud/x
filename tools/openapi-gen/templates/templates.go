// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package templates provides openapi-gen's built-in template packs.
package templates

import (
	"embed"
	"io/fs"

	"unikraft.com/x/tools/openapi-gen/generator"
)

var (
	//go:embed shared
	sharedFS embed.FS

	// TODO: embed whole directories once the response.go.tmpl symlinks, which
	// go:embed rejects, are dropped in favour of Extends.
	//
	//go:embed go-client/api.go.tmpl go-client/client.go.tmpl go-client/configuration.go.tmpl go-client/doc.go.tmpl go-client/errors.go.tmpl go-client/model.go.tmpl go-client/utils.go.tmpl
	goClientFS embed.FS

	//go:embed go-server/gin.go.tmpl go-server/header.go.tmpl go-server/model.go.tmpl go-server/model_enum.go.tmpl go-server/model_simple.go.tmpl
	goServerFS embed.FS
)

// Shared holds the templates common to GoClient and GoServer.
var Shared = &generator.Pack{Name: "shared", FS: sub(sharedFS, "shared")}

// GoClient renders a Go HTTP client.
var GoClient = &generator.Pack{Name: "go-client", FS: sub(goClientFS, "go-client"), Extends: Shared}

// GoServer renders Go models and gin handlers.
var GoServer = &generator.Pack{Name: "go-server", FS: sub(goServerFS, "go-server"), Extends: Shared}

func sub(fsys fs.FS, dir string) fs.FS {
	s, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return s
}
