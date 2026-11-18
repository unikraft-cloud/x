// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package spec holds the parsed OpenAPI types that openapi-gen passes to
// templates and to template functions.
package spec

import "github.com/getkin/kin-openapi/openapi3"

// Model represents a single model file to be generated
type Model struct {
	SchemaName string
	Schema     *openapi3.Schema
	// Package is derived from the x-package extension.
	//
	// Deprecated: x-package is only emitted by our proto-based generation
	// pipeline. Prefer Namespace, which works for any OpenAPI document.
	Package string
	// Namespace is derived from the "Namespace.Name" schema name prefix
	// (e.g. as emitted by platform-api's TypeSpec compiler).
	Namespace string
}

// PathOperation pairs an openapi3.Operation with its path and method
type PathOperation struct {
	Path      string
	Method    string
	Operation *openapi3.Operation
	// PathItem is the parent path item, retained so parameter helpers can merge
	// path-level parameters that every operation on the path inherits.
	PathItem *openapi3.PathItem
	vars     map[string]string
}

// Var returns the template variable key, or fallback when it is unset.
func (o PathOperation) Var(key, fallback string) string {
	if v, ok := o.vars[key]; ok {
		return v
	}
	return fallback
}

// SetVars sets the template variables exposed to this operation via Var.
func (o *PathOperation) SetVars(vars map[string]string) {
	o.vars = vars
}
