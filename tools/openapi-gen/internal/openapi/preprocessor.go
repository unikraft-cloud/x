// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package openapi

import (
	"fmt"
	"sort"

	"github.com/ettle/strcase"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/mitchellh/copystructure"
)

// The preprocessor rewrites an OpenAPI document so that every schema a code
// generator might need a Go type for exists as a named entry under
// components/schemas.
//
// OpenAPI lets you define schemas *inline* — directly as a property value, as
// an array's items, or as a request/response body. A generator, however, needs
// a name for each type it emits. The preprocessor therefore walks the document,
// and wherever it finds an anonymous object or enum it "hoists" it: a copy is
// registered under a synthesized name and the original location is replaced
// with a $ref to it. The result is a document where types are always reached
// through a $ref, never defined anonymously in place.
//
// This mirrors OpenAPI Generator's InlineModelResolver.

type preprocessor struct {
	doc    *openapi3.T
	parser *Parser

	// processed guards against hoisting the same schema twice (schemas can be
	// reached both as a top-level component and via recursion into a hoisted
	// copy). It is keyed by component name.
	processed map[string]bool
}

// Preprocess hoists all inline schemas in the parser's document into named
// components/schemas entries, in place.
func (p *Parser) Preprocess() error {
	return Preprocess(p.doc, p)
}

// Preprocess hoists all inline schemas in doc into named components/schemas
// entries, in place.
func Preprocess(doc *openapi3.T, parser *Parser) error {
	p := &preprocessor{
		doc:       doc,
		parser:    parser,
		processed: make(map[string]bool),
	}

	// Snapshot the author-written schema names before adding any of our own,
	// so the walk never revisits schemas created during this pass (those are
	// handled inline, as they are created).
	names := make([]string, 0, len(doc.Components.Schemas))
	for name := range doc.Components.Schemas {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if ref := doc.Components.Schemas[name]; ref != nil {
			p.hoistSchema(ref.Value, name)
		}
	}

	return p.hoistOperationBodies()
}

// hoistSchema promotes every anonymous object/enum reachable from schema's
// properties to a named component, recursing into each one it creates. name is
// schema's own component name and seeds the names of anything hoisted out of
// it (e.g. property "foo" of "Bar" becomes "BarFoo").
func (p *preprocessor) hoistSchema(schema *openapi3.Schema, name string) {
	if schema == nil || p.processed[name] {
		return
	}
	p.processed[name] = true
	pkg, _ := schema.Extensions["x-package"].(string)

	// Only properties we are free to mutate are considered: a schema's own
	// properties and those of its *inline* allOf/oneOf/anyOf branches.
	// Properties reached through a $ref branch belong to another top-level
	// schema and are hoisted when that schema is processed.
	for _, prop := range schemaProperties(schema, false) {
		p.hoistValue(prop.ref, name+strcase.ToPascal(prop.name), pkg)
	}
}

// hoistValue rewrites a single property/body slot whose value is an anonymous
// schema into a $ref to a new component named name. The owning schema's
// package is inherited by a promoted schema unless the inline schema declares
// its own package. Slots that already hold a $ref (including the allOf-wrapped
// "$ref plus description" form) or a scalar are left untouched.
func (p *preprocessor) hoistValue(slot *openapi3.SchemaRef, name, pkg string) {
	if slot == nil || slot.Ref != "" || slot.Value == nil {
		return
	}
	value := slot.Value

	switch {
	case isNameable(value):
		// An inline object or enum: hoist it directly. The slot's own
		// description (e.g. a property-specific blurb) would otherwise be
		// lost once it becomes a bare $ref, so carry it over.
		*slot = *refWithDescription(p.promote(name, value, pkg), value.Description)

	case isInlineArrayItem(value):
		// An array of inline objects/enums: hoist the item schema, leaving the
		// array itself in place.
		value.Items = p.promote(name, value.Items.Value, pkg)
	}
}

// promote registers a copy of inline as a top-level component named name,
// records its property order, recurses into it, and returns a $ref to it. A
// promoted schema inherits pkg only when it does not explicitly set
// x-package itself.
func (p *preprocessor) promote(name string, inline *openapi3.Schema, pkg string) *openapi3.SchemaRef {
	clone := cloneSchema(inline)
	if pkg != "" {
		if clone.Extensions == nil {
			clone.Extensions = make(map[string]any)
		}
		if _, ok := clone.Extensions["x-package"]; !ok {
			clone.Extensions["x-package"] = pkg
		}
	}
	p.doc.Components.Schemas[name] = &openapi3.SchemaRef{Value: clone}

	if order := schemaPropertyNames(clone); len(order) > 0 {
		p.parser.SetPropertyOrder(name, order)
	}

	p.hoistSchema(clone, name)

	return &openapi3.SchemaRef{Ref: refPath(name)}
}

// refWithDescription attaches description to ref, if non-empty. OpenAPI 3.0
// doesn't allow sibling keys next to $ref, so a non-empty description is
// attached using the conventional allOf-wrapped form instead: {allOf:
// [{$ref: ...}], description: ...}. getProperty (funcs.go) already treats
// this shape the same as a bare $ref for type resolution, so callers
// (templates) don't need to special-case it.
func refWithDescription(ref *openapi3.SchemaRef, description string) *openapi3.SchemaRef {
	if description == "" || ref.Ref == "" {
		return ref
	}
	return &openapi3.SchemaRef{Value: &openapi3.Schema{
		AllOf:       []*openapi3.SchemaRef{{Ref: ref.Ref}},
		Description: description,
	}}
}

// hoistOperationBodies hoists inline request and response body schemas, naming
// them after the operation (e.g. "GetUsersResponse").
func (p *preprocessor) hoistOperationBodies() error {
	for _, item := range p.doc.Paths.Map() {
		for _, op := range operations(item) {
			if op == nil || op.OperationID == "" {
				continue
			}
			p.hoistRequestBody(op)
			if err := p.hoistResponseBody(op); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *preprocessor) hoistRequestBody(op *openapi3.Operation) {
	if op.RequestBody == nil || op.RequestBody.Value == nil {
		return
	}
	content := op.RequestBody.Value.Content

	if json := content.Get("application/json"); json != nil {
		p.hoistBody(json.Schema, op.OperationID+"Request")
	}

	// Binary uploads carry no JSON schema; normalise them to string/binary so
	// templates can special-case them (e.g. as []byte or io.Reader).
	if bin := content.Get("application/octet-stream"); bin != nil && bin.Schema != nil && bin.Schema.Value != nil {
		s := bin.Schema.Value
		if s.Type == nil || !s.Type.Is("string") {
			s.Type = &openapi3.Types{"string"}
		}
		if s.Format == "" {
			s.Format = "binary"
		}
	}
}

func (p *preprocessor) hoistResponseBody(op *openapi3.Operation) error {
	if op.Responses == nil {
		return nil
	}

	// A unique name is only unresolvable when two or more responses carry an
	// inline JSON body that would all hoist to the same OperationID+"Response"
	// name. Responses that are $refs, have no JSON body, or hold a body that
	// hoistBody wouldn't rename anyway (e.g. a bare scalar) don't conflict, so
	// don't count them.
	var inline int
	for _, resp := range op.Responses.Map() {
		if resp == nil || resp.Value == nil {
			continue
		}
		if json := resp.Value.Content.Get("application/json"); json != nil && isHoistableBody(json.Schema) {
			inline++
		}
	}
	if inline > 1 {
		return fmt.Errorf("operation %s has multiple inline response bodies; cannot derive a unique schema name", op.OperationID)
	}

	for _, resp := range op.Responses.Map() {
		if resp == nil || resp.Value == nil {
			continue
		}
		if json := resp.Value.Content.Get("application/json"); json != nil {
			p.hoistBody(json.Schema, op.OperationID+"Response")
		}
	}
	return nil
}

// hoistBody hoists an inline body schema, mirroring property hoisting but
// suffixing array item schemas with "Item" (there is no property name to draw
// a singular from).
//
// Unlike hoistValue, this always leaves a bare $ref rather than wrapping it
// with the body's own description: templates resolve request/response body
// types by checking Schema.Ref directly (they don't go through getProperty's
// allOf-normalisation), so an allOf-wrapped ref here would break type
// resolution. No template renders a body schema's own description as a
// comment today, so nothing is lost by not preserving it.
func (p *preprocessor) hoistBody(slot *openapi3.SchemaRef, name string) {
	if !isHoistableBody(slot) {
		return
	}
	value := slot.Value

	switch {
	case isNameable(value):
		*slot = *p.promote(name, value, "")

	case isInlineArrayItem(value):
		value.Items = p.promote(name+"Item", value.Items.Value, "")
	}
}

// isHoistableBody reports whether slot holds an inline schema that hoistBody
// would actually promote to a new named component: not already a $ref, and
// itself an object/enum or an array of them. Anything else (a $ref, a bare
// scalar, ...) is left untouched by hoistBody, so it can never collide with
// another hoisted name.
func isHoistableBody(slot *openapi3.SchemaRef) bool {
	if slot == nil || slot.Ref != "" || slot.Value == nil {
		return false
	}
	return isNameable(slot.Value) || isInlineArrayItem(slot.Value)
}

// --- schema classification ---------------------------------------------------

// isObject reports whether s is an object with properties — something worth a
// named Go struct. A missing type is treated as "object", matching how
// OpenAPI specs commonly omit it.
func isObject(s *openapi3.Schema) bool {
	return (s.Type == nil || s.Type.Is("object")) && len(s.Properties) > 0
}

// isEnum reports whether s enumerates a fixed set of values.
func isEnum(s *openapi3.Schema) bool {
	return len(s.Enum) > 0
}

// isNameable reports whether an anonymous schema deserves to be promoted to a
// named component (and thus a dedicated Go type).
func isNameable(s *openapi3.Schema) bool {
	return s != nil && (isObject(s) || isEnum(s))
}

// isInlineArrayItem reports whether s is an array whose items are an inline
// (un-referenced) nameable schema.
func isInlineArrayItem(s *openapi3.Schema) bool {
	return s.Type != nil && s.Type.Is("array") &&
		s.Items != nil && s.Items.Ref == "" && isNameable(s.Items.Value)
}

// --- property traversal (shared with funcs.go) -------------------------------

// schemaProp is a property together with the schema that declares it.
type schemaProp struct {
	name  string
	ref   *openapi3.SchemaRef
	owner *openapi3.Schema
}

// schemaProperties returns every property contributed by schema, flattening
// allOf/oneOf/anyOf composition. The first occurrence of a name wins and the
// result is sorted for determinism.
//
// followRefs controls whether composition branches that are $refs are
// descended into. Pass true to see inherited properties (read-only callers);
// pass false to stay within schemas that are safe to mutate.
func schemaProperties(schema *openapi3.Schema, followRefs bool) []schemaProp {
	var props []schemaProp
	seen := make(map[string]bool)

	var walk func(owner *openapi3.Schema)
	walk = func(owner *openapi3.Schema) {
		if owner == nil {
			return
		}
		for name, ref := range owner.Properties {
			if !seen[name] {
				seen[name] = true
				props = append(props, schemaProp{name: name, ref: ref, owner: owner})
			}
		}
		for _, branch := range compositionBranches(owner) {
			if branch == nil || (branch.Ref != "" && !followRefs) {
				continue
			}
			walk(branch.Value)
		}
	}
	walk(schema)

	sort.Slice(props, func(i, j int) bool { return props[i].name < props[j].name })
	return props
}

// schemaPropertyNames returns the sorted names of every property contributed by
// schema, including via composition.
func schemaPropertyNames(schema *openapi3.Schema) []string {
	props := schemaProperties(schema, true)
	names := make([]string, len(props))
	for i, prop := range props {
		names[i] = prop.name
	}
	return names
}

// compositionBranches returns the allOf, oneOf and anyOf sub-schemas of s.
func compositionBranches(s *openapi3.Schema) []*openapi3.SchemaRef {
	branches := make([]*openapi3.SchemaRef, 0, len(s.AllOf)+len(s.OneOf)+len(s.AnyOf))
	branches = append(branches, s.AllOf...)
	branches = append(branches, s.OneOf...)
	branches = append(branches, s.AnyOf...)
	return branches
}

// --- misc helpers ------------------------------------------------------------

// operations returns the operations defined on a path item, in a fixed order.
func operations(item *openapi3.PathItem) []*openapi3.Operation {
	return []*openapi3.Operation{item.Get, item.Post, item.Put, item.Delete, item.Patch}
}

func refPath(name string) string {
	return "#/components/schemas/" + name
}

func cloneSchema(s *openapi3.Schema) *openapi3.Schema {
	clone, err := copystructure.Copy(s)
	if err != nil {
		panic(err)
	}
	return clone.(*openapi3.Schema)
}
