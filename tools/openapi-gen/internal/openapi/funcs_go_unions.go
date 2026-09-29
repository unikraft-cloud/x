// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package openapi

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ettle/strcase"
	"github.com/getkin/kin-openapi/openapi3"
)

// GoUnionVariant is one member of a closed union.
type GoUnionVariant struct {
	// Type is the Go type name the variant is referred to by.
	Type string
	// Underlying is the Go type the variant is defined in terms of.  It only
	// differs from Type for variants that Declare their own named type.
	Underlying string
	// Declare reports whether the variant needs a type declaration of its own,
	// which is the case for every branch that is not a named schema of the
	// package being generated.
	Declare bool
	// Doc is the description of the branch, if the spec gives one.
	Doc string
	// Package is the x-package, and Namespace the "Namespace.Name" prefix, of a
	// schema that lives outside the package being generated.  Union discovery
	// spans the whole document, so a branch may name a schema that
	// package/namespace filtering left in another package; such a variant is a
	// local wrapper (Declare is true) whose Underlying type templates qualify
	// with these, since a method cannot be attached to a foreign type.  Both are
	// empty for every other variant.
	Package string
	// Namespace is the "Namespace.Name" prefix of a foreign variant's schema.
	Namespace string
	// Kinds are the JSON kinds (as Go rune literals, e.g. `'{'`) the variant
	// encodes to, and which the decoder dispatches on.  Every kind of the
	// variant's single OpenAPI type is listed, so a boolean takes both `'t'` and
	// `'f'`; a branch permitting more than one type has no single Go type to
	// decode all of them into and is not made a variant at all.  A union whose
	// variants do not have kinds to themselves is not generated either, since
	// generated structs neither enforce required fields nor reject unknown ones
	// and so cannot be told apart by decoding them speculatively.
	//
	// JSON null is never among them: it is the absence of a value rather than a
	// shape, so a nullable branch is dispatched on the kinds it takes when it is
	// present, and the union as a whole decodes `null` to its nil interface.
	Kinds []string
}

// GoUnion describes the closed union generated for an `anyOf` property: a
// marker interface, a named type per variant that is not already a named schema
// of this package, and a decoder that dispatches on the JSON kind of the value.
type GoUnion struct {
	// Name is the Go name of the marker interface.
	Name string
	// TypeNames are the variant type names, in spec order.
	TypeNames []string
	// Variants are the members of the union, in spec order.
	Variants []GoUnionVariant
}

// GoUnions is the result of union discovery over every model in the document.
type GoUnions struct {
	// Unions are the discovered unions, ordered by name so that generation is
	// deterministic.
	Unions []GoUnion
	// Fields maps "SchemaName.propertyName" to the name of the union that the
	// property is typed as.  A property whose branches cannot be turned into a
	// union is absent, and stays whatever type it had before.
	Fields map[string]string
}

// goUnionKindSuffixes name the type declared for a branch that is not already a
// named schema: the string branch of an `ImageSpec` union becomes
// `ImageReference`.
var goUnionKindSuffixes = map[string]string{
	"string":  "Reference",
	"integer": "Number",
	"number":  "Number",
	"boolean": "Flag",
	"array":   "List",
	"object":  "Object",
}

// goUnionTypeKinds are the JSON kinds a branch of the given OpenAPI type
// encodes to, as the rune literals jsontext.Value.Kind reports.
var goUnionTypeKinds = map[string][]string{
	"string":  {`'"'`},
	"integer": {`'0'`},
	"number":  {`'0'`},
	"boolean": {`'t'`, `'f'`},
	"array":   {`'['`},
	"object":  {`'{'`},
}

// goUnionNullType is the type of a branch that permits JSON null, which 3.1
// lists among the others and 3.0 spells `nullable: true`.  It is not a shape a
// union dispatches on — see GoUnionVariant.Kinds — so it is dropped from the
// types of a branch rather than given a kind.
const goUnionNullType = "null"

// goUnionMaxDepth bounds how far the types of a branch are chased through
// composition, so that a recursive schema cannot loop.
const goUnionMaxDepth = 8

// goUnionKinds returns the JSON kinds a branch permitting the given OpenAPI
// types encodes to, in a stable order.  It reports false for a type it does not
// know, which is a branch to leave alone rather than one to guess the shape of:
// a wrong kind has the decoder reject or misdispatch a valid payload.
func goUnionKinds(openAPITypes []string) ([]string, bool) {
	kinds := make([]string, 0, len(openAPITypes))
	seen := map[string]bool{}

	for _, openAPIType := range openAPITypes {
		mapped, ok := goUnionTypeKinds[openAPIType]
		if !ok {
			return nil, false
		}
		for _, kind := range mapped {
			if seen[kind] {
				continue
			}
			seen[kind] = true
			kinds = append(kinds, kind)
		}
	}

	if len(kinds) == 0 {
		return nil, false
	}

	return kinds, true
}

// goUnionTypes returns every non-null JSON type a branch's schema permits, in a
// stable order.  It reports false when they cannot be derived, which is a
// branch nothing can dispatch on.
//
// What a schema permits is not always written on it as a type: 3.1 lists more
// than one, 3.0 splits the null half of it into `nullable`, an enum without a
// type takes the types of its values, an object routinely omits `type: object`,
// and a composition takes the types of what it composes.
func goUnionTypes(schema *openapi3.Schema, depth int) ([]string, bool) {
	if schema == nil || depth > goUnionMaxDepth {
		return nil, false
	}

	if schema.Type != nil && len(schema.Type.Slice()) > 0 {
		return goUnionWithoutNull(schema.Type.Slice())
	}

	switch {
	case len(schema.Enum) > 0:
		// A typeless enum is typed by its values.  Forcing it to `string` would
		// hand numeric or boolean JSON to a string-backed variant.
		types := make([]string, 0, len(schema.Enum))
		for _, value := range schema.Enum {
			openAPIType := goUnionValueType(value)
			if openAPIType == "" {
				return nil, false
			}
			types = append(types, openAPIType)
		}
		return goUnionWithoutNull(types)

	case len(schema.Properties) > 0 || schema.AdditionalProperties.Schema != nil ||
		schema.AdditionalProperties.Has != nil:
		return []string{"object"}, true

	case len(schema.AllOf) > 0:
		// Every member has to match, so the branch is of the types they all
		// permit.  A member that says nothing about the type does not narrow it.
		var types []string
		known := false
		for _, member := range schema.AllOf {
			memberTypes, ok := goUnionTypes(goUnionValueOf(member), depth+1)
			if !ok {
				continue
			}
			if !known {
				types, known = memberTypes, true
				continue
			}
			types = goUnionIntersection(types, memberTypes)
		}
		return types, len(types) > 0

	case len(schema.OneOf) > 0 || len(schema.AnyOf) > 0:
		// Any member may match, so the branch is of every type they permit.
		// One member of unknown type leaves the whole branch unknown.
		var types []string
		for _, member := range append(append([]*openapi3.SchemaRef{}, schema.OneOf...), schema.AnyOf...) {
			memberTypes, ok := goUnionTypes(goUnionValueOf(member), depth+1)
			if !ok {
				return nil, false
			}
			types = append(types, memberTypes...)
		}
		return goUnionWithoutNull(types)
	}

	return nil, false
}

// goUnionValueOf returns the schema a branch of a composition resolves to,
// which for a $ref is the schema the loader resolved it to.
func goUnionValueOf(ref *openapi3.SchemaRef) *openapi3.Schema {
	if ref == nil {
		return nil
	}
	return ref.Value
}

// goUnionWithoutNull dedupes types in the order given and drops the null type,
// reporting false when no dispatchable type is left.
func goUnionWithoutNull(types []string) ([]string, bool) {
	deduped := make([]string, 0, len(types))
	seen := map[string]bool{}

	for _, openAPIType := range types {
		if openAPIType == "" || openAPIType == goUnionNullType || seen[openAPIType] {
			continue
		}
		seen[openAPIType] = true
		deduped = append(deduped, openAPIType)
	}

	return deduped, len(deduped) > 0
}

// goUnionIntersection returns the types held by both, keeping the order of the
// first.
func goUnionIntersection(a, b []string) []string {
	inB := make(map[string]bool, len(b))
	for _, openAPIType := range b {
		inB[openAPIType] = true
	}

	both := make([]string, 0, len(a))
	for _, openAPIType := range a {
		if inB[openAPIType] {
			both = append(both, openAPIType)
		}
	}

	return both
}

// goUnionValueType returns the OpenAPI type of a value written in a spec, which
// is how a typeless enum is typed.  It is empty for a value of a shape the
// decoder gives no type to.
func goUnionValueType(value any) string {
	switch value.(type) {
	case nil:
		return goUnionNullType
	case string:
		return "string"
	case bool:
		return "boolean"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "integer"
	case float32, float64, json.Number:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return ""
	}
}

// goUnionBranch is a single resolved `anyOf` branch.
type goUnionBranch struct {
	// GoType is the Go type the branch encodes to.
	GoType string
	// Named reports whether GoType is a schema that is generated in its own
	// right, and so needs neither a declaration nor a name of our choosing.
	Named bool
	// OpenAPITypes are the non-null types permitted by the schema that actually
	// describes the branch, which for a $ref branch is the referenced schema.
	// A usable branch permits exactly one, since its Go type decodes one shape.
	OpenAPITypes []string
	// Kinds are the JSON kinds those types encode to.
	Kinds []string
	// Doc is the description given on the branch itself.
	Doc string
	// Foreign reports whether a named branch's schema is generated into a
	// package other than the one being generated, which makes it a type no
	// method of ours can be attached to.
	Foreign bool
	// Package and Namespace are where a foreign branch's schema lives, carried
	// through to the variant so templates can qualify it.
	Package   string
	Namespace string
}

// goUnionSchema is a named schema of the document together with the metadata a
// template needs to refer to it from a package other than its own.
type goUnionSchema struct {
	schema *openapi3.Schema
	pkg    string
	// selected reports whether the schema is one of the models this run
	// generates, rather than one only the document knows about.
	selected  bool
	namespace string
}

// goUnionSchemas returns every named schema of the document that generates a
// type of its own, keyed by that type name.
//
// It reads the parser's components rather than the selected models because
// FilterByPackage and FilterByNamespace narrow the models before templates run.
// A branch referencing a schema of another package is still a named schema, and
// mistaking it for an anonymous one would declare it a second time under a name
// of our choosing.
func (tf *templateFuncs) goUnionSchemas() map[string]goUnionSchema {
	schemas := map[string]goUnionSchema{}

	add := func(name, namespace string, schema *openapi3.Schema, selected bool) {
		// A schema too empty to generate a type is not a named branch: it maps
		// to interface{} wherever it is referenced.
		if name == "" || schemaIsEmpty(schema) {
			return
		}
		pkg, _ := schema.Extensions["x-package"].(string)
		schemas[name] = goUnionSchema{
			schema:    schema,
			pkg:       pkg,
			selected:  selected,
			namespace: namespace,
		}
	}

	if components := tf.components(); components != nil {
		for name, ref := range components.Schemas {
			if ref != nil {
				// Flattening strips the namespace from the component key, so
				// ask the parser rather than reading the name.
				add(name, tf.parser.schemaNamespaceOf(name), ref.Value, false)
			}
		}
	}

	if tf.models != nil {
		for _, model := range *tf.models {
			if existing, ok := schemas[model.SchemaName]; ok {
				existing.selected = true
				schemas[model.SchemaName] = existing
				continue
			}
			// A model keeps the namespace it was parsed under, whatever
			// flattening later did to its name.
			add(model.SchemaName, model.Namespace, model.Schema, true)
		}
	}

	return schemas
}

// goUnionReserved returns the type names the generated package already uses:
// every schema of the document, and the inline enums that models declare by
// title.  A union, and every branch it declares a type for, has to steer clear
// of them or the generated package does not compile.
func (tf *templateFuncs) goUnionReserved() map[string]bool {
	reserved := map[string]bool{}

	// Every component name, including those too empty to generate a type of
	// their own: a name in the document is not ours to take either way.
	if components := tf.components(); components != nil {
		for name := range components.Schemas {
			reserved[name] = true
		}
	}

	if tf.models != nil {
		for _, model := range *tf.models {
			reserved[model.SchemaName] = true
			for _, enum := range tf.inlineEnums(model.SchemaName, model.Schema) {
				reserved[enum.Name] = true
			}
		}
	}

	return reserved
}

// components returns the document's components, or nil when there are none.
func (tf *templateFuncs) components() *openapi3.Components {
	if tf.parser == nil || tf.parser.doc == nil {
		return nil
	}
	return tf.parser.doc.Components
}

// goUnions collects every property declared as an `anyOf` of two or more
// shapes and describes it as a closed union.  A property has no single Go type
// otherwise, and degrading it to `interface{}` leaves callers to hand-roll the
// encoding.
//
// Unions are named after their first named branch with a trailing "Spec"
// dropped, e.g. a "string or ImageSpec" property becomes an `ImageUnion`.  A
// name already taken by a schema or by another union falls back to one derived
// from the declaring property.  The optional overrides map replaces the derived
// name, keyed by the base it comes from ("Image" -> "ImageSource"), for the
// cases where a better name reads more naturally.
func (tf *templateFuncs) goUnions(overrides ...map[string]any) *GoUnions {
	names := map[string]string{}
	for _, override := range overrides {
		for base, name := range override {
			if name, ok := name.(string); ok {
				names[base] = name
			}
		}
	}

	unions := &GoUnions{Fields: map[string]string{}}
	if tf.models == nil {
		return unions
	}

	// Named schemas, for resolving $ref branches, and the names that generated
	// code has already claimed.  Both span the whole document rather than the
	// selected models, so that filtering does not turn a named branch into an
	// anonymous one or free up a name that is still taken.
	schemas := tf.goUnionSchemas()
	reserved := tf.goUnionReserved()

	// Unions are keyed by the shapes they hold, because two properties can share
	// a base name yet list different branches, e.g. `anyOf: [FooSpec, string]`
	// next to `anyOf: [FooSpec, boolean]`.
	byKey := map[string]string{}

	for _, model := range *tf.models {
		for _, propName := range tf.propertyNamesOrdered(model.SchemaName, model.Schema) {
			prop := tf.getProperty(model.Schema, propName)
			if prop == nil || len(prop.AnyOf) < 2 {
				continue
			}

			branches, ok := tf.goUnionBranches(schemas, prop)
			if !ok {
				continue
			}

			// A name derived from the declaring property serves both as the
			// fallback for a union with no named branch and as the tie-break for
			// a name already taken by a different set of branches.
			fallback := model.SchemaName + strcase.ToPascal(propName)

			base := goUnionBase(branches)
			if base == "" {
				base = fallback
			}
			want := base + "Union"
			if override, ok := names[base]; ok {
				want = override
			}

			key := goUnionKey(branches)
			name, ok := byKey[key]
			if !ok {
				// An identical set of branches is the same union; anything else
				// needs a name of its own.
				name, base = goUnionFreeName(want, base, fallback, reserved)
				reserved[name] = true
				byKey[key] = name
				unions.Unions = append(unions.Unions, goUnionOf(name, base, branches, reserved))
			}

			unions.Fields[model.SchemaName+"."+propName] = name
		}
	}

	sort.Slice(unions.Unions, func(i, j int) bool {
		return unions.Unions[i].Name < unions.Unions[j].Name
	})

	return unions
}

// goUnionBranches resolves each branch of an `anyOf` property to a Go type and
// to the schema that actually describes it (a branch may be a bare $ref, or the
// allOf-wrapped "$ref plus description" form).  It reports false when a branch
// has a shape that can neither be named nor dispatched on, when a branch permits
// more than one type and so more than its Go type can decode, and when two
// branches encode to the same JSON kind, in which case the property is left
// alone.
//
// A branch is named only when it references a schema; an inline one is
// described in terms of the Go type its shape encodes to, whatever title it
// carries, and the union declares a type for it.
func (tf *templateFuncs) goUnionBranches(schemas map[string]goUnionSchema, prop *openapi3.Schema) ([]goUnionBranch, bool) {
	branches := make([]goUnionBranch, 0, len(prop.AnyOf))

	for _, ref := range prop.AnyOf {
		goType, resolved := "", ref.Value
		named := false
		foreign := false
		pkg, namespace := "", ""

		// Only a reference names a schema.  A branch's `title` does not: hoisting
		// covers the schemas nested in a property, not those nested in its
		// `anyOf`, so taking a title for a type name would define the variant in
		// terms of a type that is never declared — or, where the title happens to
		// match a component, in terms of an unrelated one.
		if refName := goUnionRefName(ref); refName != "" {
			schema, ok := schemas[refName]
			if !ok {
				// The reference resolves to a schema too empty to generate a
				// type, which is interface{} wherever it appears.
				return nil, false
			}
			goType, named, resolved = refName, true, schema.schema
			if foreign = tf.goUnionForeign(schema); foreign {
				pkg, namespace = schema.pkg, schema.namespace
			} else if !schema.selected {
				// Filtering left the schema out of this run and there is no
				// package or namespace to qualify it by, so a variant naming it
				// would name a type nothing declares.
				return nil, false
			}
		} else {
			shape := goUnionShape(ref)
			// The Go type of an inline shape names the schemas its items and map
			// values reference, and a variant carries qualification for the
			// branch as a whole rather than for a type nested inside it.  So a
			// nested reference this package cannot name unqualified — one
			// filtering left in another package, or left out of the run
			// altogether — is a branch to leave alone rather than describe with
			// a type that does not compile.
			for _, nested := range goUnionNestedRefs(shape, 0) {
				schema, ok := schemas[nested]
				if !ok || !schema.selected || tf.goUnionForeign(schema) {
					return nil, false
				}
			}
			goType = tf.schemaToGoType(shape.Value)
		}

		openAPITypes, ok := goUnionTypes(resolved, 0)
		if !ok {
			return nil, false
		}

		// A branch of more than one type — a 3.1 `type: [string, object]`, a
		// typeless enum of mixed values — encodes to one Go type all the same,
		// which can hold only one of the shapes it advertises.  Dispatching the
		// others to it fails to decode, so the property is left as it was.
		if len(openAPITypes) > 1 {
			return nil, false
		}

		kinds, ok := goUnionKinds(openAPITypes)
		if !ok {
			return nil, false
		}

		if goType == "interface{}" {
			return nil, false
		}

		doc := ""
		if ref.Value != nil {
			doc = ref.Value.Description
		}

		branches = append(branches, goUnionBranch{
			GoType:       goType,
			Named:        named,
			OpenAPITypes: openAPITypes,
			Kinds:        kinds,
			Doc:          doc,
			Foreign:      foreign,
			Package:      pkg,
			Namespace:    namespace,
		})
	}

	branches = goUnionCollapsed(branches)
	if goUnionAmbiguous(branches) {
		return nil, false
	}

	return branches, true
}

// goUnionCollapsed drops the branches that repeat a schema an earlier branch
// already names, which are the same variant written twice.
func goUnionCollapsed(branches []goUnionBranch) []goUnionBranch {
	collapsed := make([]goUnionBranch, 0, len(branches))
	seen := map[string]bool{}

	for _, branch := range branches {
		if branch.Named {
			if seen[branch.GoType] {
				continue
			}
			seen[branch.GoType] = true
		}
		collapsed = append(collapsed, branch)
	}

	return collapsed
}

// goUnionAmbiguous reports whether two branches encode to the same JSON kind,
// which leaves the decoder nothing to dispatch on.  Decoding such a union
// speculatively does not work: generated structs neither enforce required
// fields nor reject unknown ones, so a `BarSpec` object decodes into `FooSpec`
// just as happily, and two scalars of the same kind are indistinguishable
// outright.  Such a property is better left as it was than silently decoded to
// the wrong variant.
func goUnionAmbiguous(branches []goUnionBranch) bool {
	seen := map[string]bool{}

	for _, branch := range branches {
		for _, kind := range branch.Kinds {
			if seen[kind] {
				return true
			}
			seen[kind] = true
		}
	}

	return false
}

// goUnionForeign reports whether a named branch's schema is generated into a
// package other than the one being generated, and so is a type this package
// cannot attach the union's marker method to.  It compares against the
// "x-package" and "current_package" template vars, the two ways a caller says
// which package it is generating; with neither set there is nothing to be
// foreign to.
func (tf *templateFuncs) goUnionForeign(schema goUnionSchema) bool {
	if tf.vars == nil {
		return false
	}
	vars := *tf.vars

	if current := vars["x-package"]; current != "" && schema.pkg != "" && schema.pkg != current {
		return true
	}
	// current_package is the lowercased namespace the caller selected, so the
	// comparison against a schema's "Namespace.Name" prefix ignores case.
	if current := vars["current_package"]; current != "" && schema.namespace != "" &&
		!strings.EqualFold(schema.namespace, current) {
		return true
	}

	return false
}

// goUnionRefName returns the schema an `anyOf` branch references, either
// directly or through the "$ref plus description" form of an allOf holding a
// single reference.  It is empty for a branch that is written out inline.
func goUnionRefName(ref *openapi3.SchemaRef) string {
	if ref == nil {
		return ""
	}
	if ref.Ref != "" {
		return extractTypeFromRef(ref.Ref)
	}
	if ref.Value != nil && len(ref.Value.AllOf) == 1 && ref.Value.AllOf[0].Ref != "" {
		return extractTypeFromRef(ref.Value.AllOf[0].Ref)
	}
	return ""
}

// goUnionNestedRefs returns the schemas an inline branch names through the array
// items and map values schemaToGoType descends into, which are the places a
// referenced type name is written into a type of the branch's own.  A reference
// nested any deeper is not one schemaToGoType reaches: an inline object is a
// `map[string]interface{}` whatever its properties reference.
func goUnionNestedRefs(ref *openapi3.SchemaRef, depth int) []string {
	if ref == nil || depth > goUnionMaxDepth {
		return nil
	}
	if depth > 0 {
		if name := goUnionRefName(ref); name != "" {
			return []string{name}
		}
	}
	if ref.Value == nil {
		return nil
	}

	names := goUnionNestedRefs(ref.Value.Items, depth+1)
	return append(names, goUnionNestedRefs(ref.Value.AdditionalProperties.Schema, depth+1)...)
}

// goUnionShape copies ref stripped of what would make schemaToGoType describe an
// inline branch as something other than the shape it encodes to, in the schema
// itself and in the array items and additionalProperties it descends into.  A
// $ref is returned untouched, being the one place the name of a type is written
// down for us.
//
// Two things are stripped.  A title, which schemaToGoType reads as the name of a
// declared type, though nothing declares it: hoisting reaches the schemas nested
// in a property, not those nested in its `anyOf`.  And the values of an enum,
// which schemaToGoType answers with `string` however the enum is typed, which
// would have the decoder hand numeric or boolean JSON to a string-backed
// variant.  An enum written without a type is given the one its values have
// before they go, so that it too encodes to what it holds.
func goUnionShape(ref *openapi3.SchemaRef) *openapi3.SchemaRef {
	if ref == nil || ref.Ref != "" || ref.Value == nil {
		return ref
	}

	value := *ref.Value
	value.Title = ""
	// A 3.1 branch lists `null` among its types, which schemaToGoType reads as
	// a type of its own and gives up on.  Null is not a shape the union
	// dispatches on, so the branch encodes to what it is when it is present.
	if value.Type != nil {
		if types, ok := goUnionWithoutNull(value.Type.Slice()); ok {
			value.Type = (*openapi3.Types)(&types)
		}
	}
	if len(value.Enum) > 0 {
		if value.Type == nil || len(value.Type.Slice()) == 0 {
			// Values of more than one type leave the branch typeless, and so
			// unusable, rather than typed as whichever came first.
			if types, ok := goUnionTypes(&value, 0); ok && len(types) == 1 {
				value.Type = &openapi3.Types{types[0]}
			}
		}
		value.Enum = nil
	}
	value.Items = goUnionShape(value.Items)
	value.AdditionalProperties.Schema = goUnionShape(value.AdditionalProperties.Schema)

	return &openapi3.SchemaRef{Value: &value}
}

// goUnionBase returns the name the union and its declared variants derive from:
// the name every branch shares when all of them are named, and otherwise the
// first named branch with a trailing "Spec" dropped.  It is empty when no
// branch is a named schema.
func goUnionBase(branches []goUnionBranch) string {
	if base := goUnionSharedBase(branches); base != "" {
		return base
	}

	for _, branch := range branches {
		if !branch.Named {
			continue
		}
		base := strings.TrimSuffix(branch.GoType, "Spec")
		if base == "" {
			continue
		}
		return base
	}
	return ""
}

// goUnionSharedBase returns the leading words every branch name shares, which
// only an all-named union can have: an anonymous branch is named after the base
// rather than contributing to it.
func goUnionSharedBase(branches []goUnionBranch) string {
	if len(branches) < 2 {
		return ""
	}

	var shared []string
	for i, branch := range branches {
		if !branch.Named {
			return ""
		}

		words := strings.Split(strcase.ToKebab(branch.GoType), "-")
		if i > 0 {
			words = commonPrefix(shared, words)
		}
		if len(words) == 0 {
			return ""
		}
		shared = words
	}

	// ToKebab lowercases, so the base is the name cut to the shared words'
	// length rather than those words rejoined.
	name := branches[0].GoType
	n := len(strings.Join(shared, ""))
	if n > len(name) {
		return ""
	}

	base := name[:n]
	for _, branch := range branches {
		// A variant sharing its union's name would redeclare it.
		if branch.GoType == base {
			return ""
		}
	}

	return base
}

// goUnionKey identifies a union by the shapes it holds.  The branches are
// sorted into the key: `anyOf` gives their order no meaning, so
// `[FooSpec, string]` and `[string, FooSpec]` are the same set of shapes and
// have to reuse one union rather than declare two under different names.
func goUnionKey(branches []goUnionBranch) string {
	tokens := make([]string, 0, len(branches))
	for _, branch := range branches {
		tokens = append(tokens, fmt.Sprintf("%s:%s", branch.GoType, strings.Join(branch.OpenAPITypes, ",")))
	}
	sort.Strings(tokens)
	return strings.Join(tokens, "|")
}

// goUnionFreeName returns the first union name that no generated type has taken,
// preferring want and otherwise deriving one from the declaring property.  It
// returns the stem the union's declared variants are named from alongside it, so
// that the two stay in step.
//
// Neither want nor the fallback is unique by construction: two schemas can share
// a base name, and two property spellings ("foo-bar" and "foo_bar") pascal-case
// to the same name.  So probe until a name is free rather than assume the first
// candidate is — a union that adopted a name already in use would be generated
// with another union's branches.
func goUnionFreeName(want, base, fallback string, used map[string]bool) (string, string) {
	if !used[want] {
		return want, base
	}
	if name := fallback + "Union"; !used[name] {
		return name, fallback
	}
	for n := 2; ; n++ {
		stem := fmt.Sprintf("%s%d", fallback, n)
		if name := stem + "Union"; !used[name] {
			return name, stem
		}
	}
}

// goUnionFreeType returns the first variant type name that is free, probing with
// a numeric suffix.  A kind suffix is not unique: `integer` and `number` share
// one, as do two inline branches of the same kind, and the name it forms may
// already belong to a schema declared elsewhere in the document.
func goUnionFreeType(want string, used func(string) bool) string {
	if !used(want) {
		return want
	}
	for n := 2; ; n++ {
		if name := fmt.Sprintf("%s%d", want, n); !used(name) {
			return name
		}
	}
}

// goUnionOf turns resolved branches into the union that holds them, claiming the
// name of every type it declares in reserved.  Branches arrive already collapsed
// and unambiguous, so every variant keeps all the JSON kinds it encodes to.
func goUnionOf(name, base string, branches []goUnionBranch, reserved map[string]bool) GoUnion {
	union := GoUnion{Name: name}

	typesSeen := map[string]bool{}
	free := func(want string) string {
		goType := goUnionFreeType(want, func(candidate string) bool {
			return typesSeen[candidate] || reserved[candidate]
		})
		reserved[goType] = true
		return goType
	}

	for _, branch := range branches {
		goType := branch.GoType
		switch {
		case branch.Named && !branch.Foreign:
			// A schema of this package is generated already, and the marker
			// method can be declared on it directly.
		case branch.Named:
			// The union's marker method cannot be attached to a type of another
			// package, so a foreign schema joins the union through a local type
			// defined in terms of it, which templates qualify.
			goType = free(branch.GoType + "Variant")
		default:
			// A declared variant needs a name that is free both within this
			// union and across the generated package.  A branch has one type by
			// the time it gets here, so that type names it.
			goType = free(base + goUnionKindSuffixes[branch.OpenAPITypes[0]])
		}
		typesSeen[goType] = true

		union.TypeNames = append(union.TypeNames, goType)
		union.Variants = append(union.Variants, GoUnionVariant{
			Type:       goType,
			Underlying: branch.GoType,
			Declare:    !branch.Named || branch.Foreign,
			Doc:        branch.Doc,
			Package:    branch.Package,
			Namespace:  branch.Namespace,
			Kinds:      branch.Kinds,
		})
	}

	return union
}

// commonPrefix returns the leading elements a and b share.
func commonPrefix[T comparable](a, b []T) []T {
	a = a[:min(len(a), len(b))]
	for i := range a {
		if a[i] != b[i] {
			return a[:i]
		}
	}
	return a
}
