// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package openapi

import (
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// flattenMode controls how namespaced schema names (e.g. "Instances.Instance")
// are rewritten into valid Go identifiers.
type flattenMode int

const (
	// flattenNone leaves names untouched.
	flattenNone flattenMode = iota
	// flattenStrip drops the namespace prefix: "Instances.Instance" -> "Instance".
	// Used for single-package output where every type lives together.
	flattenStrip
	// flattenJoin concatenates the segments: "Instances.Instance" -> "InstancesInstance".
	// Used when domain-prefixed names are desired to avoid collisions.
	flattenJoin
)

func flattenModeFromString(s string) flattenMode {
	switch s {
	case "strip", "true":
		return flattenStrip
	case "join":
		return flattenJoin
	default:
		return flattenNone
	}
}

// Flatten rewrites namespaced schema names (e.g. "Instances.Instance") in the
// parsed document and in models into valid Go identifiers according to mode
// ("strip" drops the namespace prefix, "join" concatenates segments, "" is a
// no-op). It runs after any namespace/tag filtering so those filters can match
// on the original "Namespace.Name" form, and it updates the given models in
// place to stay in sync with the rewritten document.
func (p *Parser) Flatten(models []Model, mode string) {
	m := flattenModeFromString(mode)
	if m == flattenNone {
		return
	}
	flattenNamespaces(p.doc, p, m)
	for i := range models {
		models[i].SchemaName = flattenName(models[i].SchemaName, m)
	}
}

// schemaNamespace returns the namespace prefix of a schema name, or "" if the
// name is not namespaced. e.g. "Instances.Instance" -> "Instances".
func schemaNamespace(name string) string {
	prefix, _, found := strings.Cut(name, ".")
	if !found {
		return ""
	}
	return prefix
}

// flattenName transforms a possibly-namespaced schema name per mode. Names
// without a namespace prefix are returned unchanged, making the operation
// idempotent and safe to apply more than once.
func flattenName(name string, mode flattenMode) string {
	if mode == flattenNone || !strings.Contains(name, ".") {
		return name
	}
	switch mode {
	case flattenStrip:
		return name[strings.LastIndex(name, ".")+1:]
	case flattenJoin:
		return strings.ReplaceAll(name, ".", "")
	default:
		return name
	}
}

const schemaRefPrefix = "#/components/schemas/"

func flattenRefString(ref string, mode flattenMode) string {
	if ref == "" || !strings.HasPrefix(ref, schemaRefPrefix) {
		return ref
	}
	return schemaRefPrefix + flattenName(strings.TrimPrefix(ref, schemaRefPrefix), mode)
}

// flattenNamespaces rewrites every namespaced schema name in the document —
// component keys, property orders, $ref targets and inline Title type names —
// according to mode. After this pass, all type names are valid Go identifiers
// and the rest of the generator can remain namespace-agnostic.
func flattenNamespaces(doc *openapi3.T, parser *Parser, mode flattenMode) {
	if mode == flattenNone || doc.Components == nil {
		return
	}

	// Rename component schema keys, flattening namespaced names (e.g.
	// "Instances.Instance" -> "Instance" or "InstancesInstance" per mode) into
	// valid Go identifiers. This function only renames; it records no
	// namespace/package info. Cross-package qualification, where needed, is
	// handled separately via the "x-package" extension and the
	// "current_package" template var (see getTypePackage).
	renamed := make(openapi3.Schemas, len(doc.Components.Schemas))
	for name, ref := range doc.Components.Schemas {
		flat := flattenName(name, mode)
		renamed[flat] = ref
	}
	doc.Components.Schemas = renamed

	// Rename registered property orders.
	if parser != nil && parser.propertyOrders != nil {
		newOrders := make(map[string][]string, len(parser.propertyOrders))
		for name, order := range parser.propertyOrders {
			newOrders[flattenName(name, mode)] = order
		}
		parser.propertyOrders = newOrders
	}

	// Rewrite refs and inline Titles throughout the document. A shared seen set
	// keeps shared/recursive schema pointers from being visited twice.
	seen := make(map[*openapi3.Schema]bool)
	for _, ref := range doc.Components.Schemas {
		flattenSchemaRef(ref, mode, seen)
	}
	if doc.Paths != nil {
		for _, pathItem := range doc.Paths.Map() {
			for _, op := range pathItem.Operations() {
				flattenOperation(op, mode, seen)
			}
		}
	}
}

func flattenSchemaRef(ref *openapi3.SchemaRef, mode flattenMode, seen map[*openapi3.Schema]bool) {
	if ref == nil {
		return
	}
	ref.Ref = flattenRefString(ref.Ref, mode)

	s := ref.Value
	if s == nil || seen[s] {
		return
	}
	seen[s] = true

	// Title doubles as the Go type name for inline enums/objects.
	if s.Title != "" {
		s.Title = flattenName(s.Title, mode)
	}

	for _, p := range s.Properties {
		flattenSchemaRef(p, mode, seen)
	}
	for _, a := range s.AllOf {
		flattenSchemaRef(a, mode, seen)
	}
	for _, a := range s.OneOf {
		flattenSchemaRef(a, mode, seen)
	}
	for _, a := range s.AnyOf {
		flattenSchemaRef(a, mode, seen)
	}
	if s.Not != nil {
		flattenSchemaRef(s.Not, mode, seen)
	}
	if s.Items != nil {
		flattenSchemaRef(s.Items, mode, seen)
	}
	if s.AdditionalProperties.Schema != nil {
		flattenSchemaRef(s.AdditionalProperties.Schema, mode, seen)
	}
}

func flattenOperation(op *openapi3.Operation, mode flattenMode, seen map[*openapi3.Schema]bool) {
	if op == nil {
		return
	}
	for _, pref := range op.Parameters {
		if pref.Value != nil && pref.Value.Schema != nil {
			flattenSchemaRef(pref.Value.Schema, mode, seen)
		}
	}
	if op.RequestBody != nil && op.RequestBody.Value != nil {
		for _, c := range op.RequestBody.Value.Content {
			if c.Schema != nil {
				flattenSchemaRef(c.Schema, mode, seen)
			}
		}
	}
	if op.Responses != nil {
		for _, r := range op.Responses.Map() {
			if r == nil || r.Value == nil {
				continue
			}
			for _, c := range r.Value.Content {
				if c.Schema != nil {
					flattenSchemaRef(c.Schema, mode, seen)
				}
			}
		}
	}
}
