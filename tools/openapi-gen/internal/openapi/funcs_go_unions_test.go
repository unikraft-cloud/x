// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package openapi

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// unionFuncs returns template funcs over a document holding just the given
// models, with property order taken from the models themselves.
func unionFuncs(models ...Model) *templateFuncs {
	schemas := openapi3.Schemas{}
	for _, model := range models {
		schemas[model.SchemaName] = &openapi3.SchemaRef{Value: model.Schema}
	}

	parser := &Parser{
		doc:            &openapi3.T{Components: &openapi3.Components{Schemas: schemas}},
		propertyOrders: map[string][]string{},
	}

	return &templateFuncs{parser: parser, models: &models}
}

// objectModel returns a named object schema with the given properties, in the
// order they are given.
func objectModel(name string, props ...prop) Model {
	schema := &openapi3.Schema{Type: strType("object"), Properties: openapi3.Schemas{}}
	for _, p := range props {
		schema.Properties[p.name] = &openapi3.SchemaRef{Value: p.schema}
	}
	return Model{SchemaName: name, Schema: schema}
}

type prop struct {
	name   string
	schema *openapi3.Schema
}

func anyOf(refs ...*openapi3.SchemaRef) *openapi3.Schema {
	return &openapi3.Schema{AnyOf: refs}
}

func inline(schema *openapi3.Schema) *openapi3.SchemaRef {
	return &openapi3.SchemaRef{Value: schema}
}

func ref(name string) *openapi3.SchemaRef {
	return &openapi3.SchemaRef{Ref: "#/components/schemas/" + name}
}

// resolvedRef is a $ref whose target the loader has already resolved, as every
// $ref in a loaded document is.
func resolvedRef(name string, schema *openapi3.Schema) *openapi3.SchemaRef {
	return &openapi3.SchemaRef{Ref: "#/components/schemas/" + name, Value: schema}
}

// withPackage tags a schema with the legacy x-package extension.
func withPackage(schema *openapi3.Schema, pkg string) *openapi3.Schema {
	schema.Extensions = map[string]any{"x-package": pkg}
	return schema
}

func imageSpec() Model {
	return objectModel("ImageSpec", prop{"url", &openapi3.Schema{Type: strType("string")}})
}

func fooSpec() Model {
	return objectModel("FooSpec", prop{"foo", &openapi3.Schema{Type: strType("string")}})
}

func barSpec() Model {
	return objectModel("BarSpec", prop{"bar", &openapi3.Schema{Type: strType("string")}})
}

// namedModel returns a model for a named schema of any shape, for the branches
// whose kind is derived from something other than a written type.
func namedModel(name string, schema *openapi3.Schema) Model {
	return Model{SchemaName: name, Schema: schema}
}

// scalarModel returns a named schema that is a scalar rather than an object, so
// that a union can hold two named branches without them sharing a JSON kind.
func scalarModel(name, openAPIType string) Model {
	return Model{SchemaName: name, Schema: &openapi3.Schema{Type: strType(openAPIType)}}
}

// generating tells tf which package it is generating, which is how a named
// branch of another package is recognised as foreign.
func generating(tf *templateFuncs, vars map[string]string) *templateFuncs {
	tf.vars = &vars
	return tf
}

// unionByName returns the named union, failing the test when it is absent.
func unionByName(t *testing.T, unions *GoUnions, name string) GoUnion {
	t.Helper()
	for _, union := range unions.Unions {
		if union.Name == name {
			return union
		}
	}
	t.Fatalf("union %q not generated, got %v", name, unionNames(unions))
	return GoUnion{}
}

func unionNames(unions *GoUnions) []string {
	names := make([]string, 0, len(unions.Unions))
	for _, union := range unions.Unions {
		names = append(names, union.Name)
	}
	return names
}

func TestGoUnionsNaming(t *testing.T) {
	tf := unionFuncs(
		imageSpec(),
		objectModel("Thing", prop{"image", anyOf(
			inline(&openapi3.Schema{Type: strType("string"), Description: "A plain reference."}),
			ref("ImageSpec"),
		)}),
	)

	unions := tf.goUnions()

	// Named after the first named branch, with the trailing "Spec" dropped.
	union := unionByName(t, unions, "ImageUnion")
	if got, want := unions.Fields["Thing.image"], "ImageUnion"; got != want {
		t.Fatalf("Thing.image: got %q, want %q", got, want)
	}
	if got, want := len(union.Variants), 2; got != want {
		t.Fatalf("variants: got %d, want %d", got, want)
	}

	// The string branch is declared for us; ImageSpec is generated already.
	declared := union.Variants[0]
	if declared.Type != "ImageReference" || declared.Underlying != "string" || !declared.Declare {
		t.Fatalf("declared variant: got %+v", declared)
	}
	if declared.Doc != "A plain reference." {
		t.Fatalf("declared variant doc: got %q", declared.Doc)
	}
	if got, want := declared.Kinds, []string{`'"'`}; !equal(got, want) {
		t.Fatalf("declared variant kinds: got %v, want %v", got, want)
	}

	named := union.Variants[1]
	if named.Type != "ImageSpec" || named.Declare {
		t.Fatalf("named variant: got %+v", named)
	}
	if got, want := named.Kinds, []string{`'{'`}; !equal(got, want) {
		t.Fatalf("named variant kinds: got %v, want %v", got, want)
	}
}

func TestGoUnionsNameOverride(t *testing.T) {
	tf := unionFuncs(
		imageSpec(),
		objectModel("Thing", prop{"image", anyOf(
			inline(&openapi3.Schema{Type: strType("string")}),
			ref("ImageSpec"),
		)}),
	)

	unions := tf.goUnions(map[string]any{"Image": "ImageSource"})

	unionByName(t, unions, "ImageSource")
	if got, want := unions.Fields["Thing.image"], "ImageSource"; got != want {
		t.Fatalf("Thing.image: got %q, want %q", got, want)
	}
}

// A union takes its name from the FIRST named branch, not the last.
func TestGoUnionsNamedAfterFirstNamedBranch(t *testing.T) {
	tf := unionFuncs(
		fooSpec(),
		scalarModel("BarName", "string"),
		objectModel("Thing", prop{"target", anyOf(ref("FooSpec"), ref("BarName"))}),
	)

	unions := tf.goUnions()

	unionByName(t, unions, "FooUnion")
	if got, want := len(unions.Unions), 1; got != want {
		t.Fatalf("unions: got %v, want %d", unionNames(unions), want)
	}
}

// Two object branches encode to the same JSON kind, and generated structs accept
// each other's payloads — they neither enforce required fields nor reject unknown
// ones — so there is nothing to dispatch on and no union to generate.
func TestGoUnionsRejectsSharedObjectKind(t *testing.T) {
	tf := unionFuncs(
		fooSpec(),
		barSpec(),
		objectModel("Thing", prop{"target", anyOf(ref("FooSpec"), ref("BarSpec"))}),
	)

	unions := tf.goUnions()

	if len(unions.Unions) != 0 {
		t.Fatalf("unions: got %v, want none", unionNames(unions))
	}
	if name, ok := unions.Fields["Thing.target"]; ok {
		t.Fatalf("Thing.target: got %q, want no union", name)
	}
}

// Scalars of a shared kind are no more distinguishable: `integer` and `number`
// both encode to a JSON number.
func TestGoUnionsRejectsSharedScalarKind(t *testing.T) {
	tf := unionFuncs(
		objectModel("Thing", prop{"amount", anyOf(
			inline(&openapi3.Schema{Type: strType("integer")}),
			inline(&openapi3.Schema{Type: strType("number")}),
		)}),
	)

	unions := tf.goUnions()

	if len(unions.Unions) != 0 {
		t.Fatalf("unions: got %v, want none", unionNames(unions))
	}
	if name, ok := unions.Fields["Thing.amount"]; ok {
		t.Fatalf("Thing.amount: got %q, want no union", name)
	}
}

// Two properties that share a base name but list different branches get a union
// each, rather than the second silently reusing the first.
func TestGoUnionsDistinctBranchesShareNoUnion(t *testing.T) {
	tf := unionFuncs(
		fooSpec(),
		objectModel("Thing",
			prop{"labels", anyOf(ref("FooSpec"), inline(&openapi3.Schema{Type: strType("string")}))},
			prop{"flags", anyOf(ref("FooSpec"), inline(&openapi3.Schema{Type: strType("boolean")}))},
		),
	)
	tf.parser.propertyOrders["Thing"] = []string{"labels", "flags"}

	unions := tf.goUnions()

	if got, want := unions.Fields["Thing.labels"], "FooUnion"; got != want {
		t.Fatalf("Thing.labels: got %q, want %q", got, want)
	}
	// "FooUnion" is taken by a different set of branches, so the name falls
	// back to the declaring property.
	if got, want := unions.Fields["Thing.flags"], "ThingFlagsUnion"; got != want {
		t.Fatalf("Thing.flags: got %q, want %q", got, want)
	}

	labels := unionByName(t, unions, "FooUnion")
	if got, want := labels.TypeNames, []string{"FooSpec", "FooReference"}; !equal(got, want) {
		t.Fatalf("FooUnion variants: got %v, want %v", got, want)
	}
	flags := unionByName(t, unions, "ThingFlagsUnion")
	if got, want := flags.TypeNames, []string{"FooSpec", "ThingFlagsFlag"}; !equal(got, want) {
		t.Fatalf("ThingFlagsUnion variants: got %v, want %v", got, want)
	}
}

// An identical set of branches is one union, however many properties declare it.
func TestGoUnionsIdenticalBranchesReuseUnion(t *testing.T) {
	tf := unionFuncs(
		imageSpec(),
		objectModel("Thing", prop{"image", anyOf(
			inline(&openapi3.Schema{Type: strType("string")}),
			ref("ImageSpec"),
		)}),
		objectModel("OtherThing", prop{"rom", anyOf(
			inline(&openapi3.Schema{Type: strType("string")}),
			ref("ImageSpec"),
		)}),
	)

	unions := tf.goUnions()

	if got, want := len(unions.Unions), 1; got != want {
		t.Fatalf("unions: got %v, want %d", unionNames(unions), want)
	}
	if got, want := unions.Fields["OtherThing.rom"], "ImageUnion"; got != want {
		t.Fatalf("OtherThing.rom: got %q, want %q", got, want)
	}
}

// A declared variant is defined in terms of the Go type its shape encodes to, and
// named after the kind it holds.
func TestGoUnionsDeclaredVariantUnderlyingTypes(t *testing.T) {
	tf := unionFuncs(
		objectModel("Thing", prop{"amount", anyOf(
			inline(&openapi3.Schema{Type: strType("integer"), Format: "int64"}),
			inline(&openapi3.Schema{Type: strType("string")}),
		)}),
	)

	union := unionByName(t, tf.goUnions(), "ThingAmountUnion")

	if got, want := union.TypeNames, []string{"ThingAmountNumber", "ThingAmountReference"}; !equal(got, want) {
		t.Fatalf("variants: got %v, want %v", got, want)
	}
	if got, want := union.Variants[0].Underlying, "int64"; got != want {
		t.Fatalf("first underlying: got %q, want %q", got, want)
	}
	if got, want := union.Variants[1].Underlying, "string"; got != want {
		t.Fatalf("second underlying: got %q, want %q", got, want)
	}
}

// An inline enum is backed by the type of its values, not by string: dispatching
// a JSON number to a string-backed variant fails to decode.
func TestGoUnionsInlineTypedEnumKeepsBaseType(t *testing.T) {
	tf := unionFuncs(
		objectModel("Thing", prop{"level", anyOf(
			inline(&openapi3.Schema{Type: strType("integer"), Enum: []any{1, 2}}),
			inline(&openapi3.Schema{Type: strType("string")}),
		)}),
	)

	union := unionByName(t, tf.goUnions(), "ThingLevelUnion")

	numeric := union.Variants[0]
	if got, want := numeric.Underlying, "int"; got != want {
		t.Fatalf("enum underlying: got %q, want %q", got, want)
	}
	if got, want := numeric.Kinds, []string{`'0'`}; !equal(got, want) {
		t.Fatalf("enum kinds: got %v, want %v", got, want)
	}
}

// An enum with no type of its own is string-backed, string being all it can be.
func TestGoUnionsInlineTypelessEnumIsString(t *testing.T) {
	tf := unionFuncs(
		objectModel("Thing", prop{"level", anyOf(
			inline(&openapi3.Schema{Enum: []any{"low", "high"}}),
			inline(&openapi3.Schema{Type: strType("boolean")}),
		)}),
	)

	union := unionByName(t, tf.goUnions(), "ThingLevelUnion")

	enum := union.Variants[0]
	if got, want := enum.Underlying, "string"; got != want {
		t.Fatalf("enum underlying: got %q, want %q", got, want)
	}
	if got, want := enum.Kinds, []string{`'"'`}; !equal(got, want) {
		t.Fatalf("enum kinds: got %v, want %v", got, want)
	}
}

// An array of a typed enum is backed by that type too, the enum being nested in
// the branch rather than the branch itself.
func TestGoUnionsInlineEnumItemsKeepBaseType(t *testing.T) {
	tf := unionFuncs(
		objectModel("Thing", prop{"levels", anyOf(
			inline(&openapi3.Schema{
				Type:  strType("array"),
				Items: inline(&openapi3.Schema{Type: strType("integer"), Enum: []any{1, 2}}),
			}),
			inline(&openapi3.Schema{Type: strType("string")}),
		)}),
	)

	union := unionByName(t, tf.goUnions(), "ThingLevelsUnion")

	if got, want := union.Variants[0].Underlying, "[]int"; got != want {
		t.Fatalf("array underlying: got %q, want %q", got, want)
	}
}

// Two branches naming the same schema are the same variant.
func TestGoUnionsRepeatedNamedBranchCollapses(t *testing.T) {
	tf := unionFuncs(
		fooSpec(),
		objectModel("Thing", prop{"target", anyOf(ref("FooSpec"), ref("FooSpec"))}),
	)

	union := unionByName(t, tf.goUnions(), "FooUnion")

	if got, want := union.TypeNames, []string{"FooSpec"}; !equal(got, want) {
		t.Fatalf("variants: got %v, want %v", got, want)
	}
}

// A branch of unknown shape can neither be named nor dispatched on, so the
// property is left as it was.
func TestGoUnionsSkipsUnusableBranches(t *testing.T) {
	tf := unionFuncs(
		imageSpec(),
		objectModel("Thing", prop{"image", anyOf(
			ref("ImageSpec"),
			inline(&openapi3.Schema{}),
		)}),
	)

	unions := tf.goUnions()

	if len(unions.Unions) != 0 {
		t.Fatalf("unions: got %v, want none", unionNames(unions))
	}
	if name, ok := unions.Fields["Thing.image"]; ok {
		t.Fatalf("Thing.image: got %q, want no union", name)
	}
}

// A single-branch `anyOf` is not a union.
func TestGoUnionsIgnoresSingleBranch(t *testing.T) {
	tf := unionFuncs(
		imageSpec(),
		objectModel("Thing", prop{"image", anyOf(ref("ImageSpec"))}),
	)

	if unions := tf.goUnions(); len(unions.Unions) != 0 {
		t.Fatalf("unions: got %v, want none", unionNames(unions))
	}
}

// Generation order does not depend on map iteration order.
func TestGoUnionsSortedByName(t *testing.T) {
	tf := unionFuncs(
		objectModel("Thing",
			prop{"zeta", anyOf(
				inline(&openapi3.Schema{Type: strType("string")}),
				inline(&openapi3.Schema{Type: strType("boolean")}),
			)},
			prop{"alpha", anyOf(
				inline(&openapi3.Schema{Type: strType("integer")}),
				inline(&openapi3.Schema{Type: strType("array"), Items: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: strType("string")}}}),
			)},
		),
	)
	tf.parser.propertyOrders["Thing"] = []string{"zeta", "alpha"}

	unions := tf.goUnions()

	if got, want := unionNames(unions), []string{"ThingAlphaUnion", "ThingZetaUnion"}; !equal(got, want) {
		t.Fatalf("unions: got %v, want %v", got, want)
	}
}

// Package and namespace filtering narrows the models before templates run, but
// a branch referencing a schema that filtering left out is still a named schema
// and must not be declared a second time under a name of our choosing.  Being of
// another package, it joins the union through a local wrapper: the marker method
// cannot be attached to a type declared elsewhere.
func TestGoUnionsWrapsBranchOfAnotherPackage(t *testing.T) {
	foreign := withPackage(imageSpec().Schema, "images")

	// Only Thing survived filtering; ImageSpec is generated in another package.
	tf := generating(unionFuncs(objectModel("Thing", prop{"image", anyOf(
		inline(&openapi3.Schema{Type: strType("string")}),
		resolvedRef("ImageSpec", foreign),
	)})), map[string]string{"x-package": "instances"})
	tf.parser.doc.Components.Schemas["ImageSpec"] = &openapi3.SchemaRef{Value: foreign}

	unions := tf.goUnions()

	union := unionByName(t, unions, "ImageUnion")
	if got, want := unions.Fields["Thing.image"], "ImageUnion"; got != want {
		t.Fatalf("Thing.image: got %q, want %q", got, want)
	}
	if got, want := union.TypeNames, []string{"ImageReference", "ImageSpecVariant"}; !equal(got, want) {
		t.Fatalf("variants: got %v, want %v", got, want)
	}

	// The wrapper is declared in terms of the foreign schema, and keeps the
	// metadata a template needs to qualify it.
	wrapper := union.Variants[1]
	if !wrapper.Declare || wrapper.Underlying != "ImageSpec" {
		t.Fatalf("wrapper variant: got %+v", wrapper)
	}
	if got, want := wrapper.Package, "images"; got != want {
		t.Fatalf("wrapper variant package: got %q, want %q", got, want)
	}
}

// A branch of the package being generated is generated already: the marker method
// goes on the schema's own type, with nothing to qualify.
func TestGoUnionsBranchOfSamePackageNeedsNoWrapper(t *testing.T) {
	local := withPackage(imageSpec().Schema, "images")

	tf := generating(unionFuncs(
		Model{SchemaName: "ImageSpec", Schema: local},
		objectModel("Thing", prop{"image", anyOf(
			inline(&openapi3.Schema{Type: strType("string")}),
			resolvedRef("ImageSpec", local),
		)}),
	), map[string]string{"x-package": "images"})

	union := unionByName(t, tf.goUnions(), "ImageUnion")

	named := union.Variants[1]
	if named.Type != "ImageSpec" || named.Declare {
		t.Fatalf("named variant: got %+v", named)
	}
	if named.Package != "" || named.Namespace != "" {
		t.Fatalf("named variant: got %+v, want no package metadata", named)
	}
}

// A branch of another namespace is foreign for the same reason a branch of
// another package is, and carries its namespace through for qualifying.
func TestGoUnionsWrapsBranchOfAnotherNamespace(t *testing.T) {
	foreign := imageSpec().Schema

	tf := generating(unionFuncs(objectModel("Thing", prop{"image", anyOf(
		inline(&openapi3.Schema{Type: strType("string")}),
		resolvedRef("Images.ImageSpec", foreign),
	)})), map[string]string{"current_package": "core"})
	tf.parser.doc.Components.Schemas["Images.ImageSpec"] = &openapi3.SchemaRef{Value: foreign}

	unions := tf.goUnions()

	union := unionByName(t, unions, unions.Fields["Thing.image"])
	wrapper := union.Variants[1]
	if !wrapper.Declare {
		t.Fatalf("wrapper variant: got %+v, want a declaration", wrapper)
	}
	if got, want := wrapper.Namespace, "Images"; got != want {
		t.Fatalf("wrapper variant namespace: got %q, want %q", got, want)
	}
}

// A branch of the namespace being generated is local, however the caller cased
// the namespace it selected.
func TestGoUnionsBranchOfSameNamespaceNeedsNoWrapper(t *testing.T) {
	local := imageSpec().Schema

	tf := generating(unionFuncs(
		Model{SchemaName: "Images.ImageSpec", Schema: local, Namespace: "Images"},
		objectModel("Images.Thing", prop{"image", anyOf(
			inline(&openapi3.Schema{Type: strType("string")}),
			resolvedRef("Images.ImageSpec", local),
		)}),
	), map[string]string{"current_package": "images"})

	unions := tf.goUnions()

	union := unionByName(t, unions, unions.Fields["Images.Thing.image"])
	named := union.Variants[1]
	if named.Declare || named.Namespace != "" {
		t.Fatalf("named variant: got %+v, want no declaration", named)
	}
}

// A branch naming a schema that filtering left out, with no package or namespace
// to qualify it by, would name a type nothing declares, so the property is left
// as it was rather than generated into code that does not compile.
func TestGoUnionsSkipsUnqualifiableFilteredBranch(t *testing.T) {
	absent := imageSpec().Schema

	tf := unionFuncs(objectModel("Thing", prop{"image", anyOf(
		inline(&openapi3.Schema{Type: strType("string")}),
		resolvedRef("ImageSpec", absent),
	)}))
	tf.parser.doc.Components.Schemas["ImageSpec"] = &openapi3.SchemaRef{Value: absent}

	unions := tf.goUnions()

	if len(unions.Unions) != 0 {
		t.Fatalf("unions: got %v, want none", unionNames(unions))
	}
	if name, ok := unions.Fields["Thing.image"]; ok {
		t.Fatalf("Thing.image: got %q, want no union", name)
	}
}

// Flattening rewrites the namespace out of a schema's name, but a variant still
// has to carry it for a template to qualify a type from another namespace.
func TestGoUnionsVariantKeepsNamespaceAfterFlatten(t *testing.T) {
	foreign := imageSpec().Schema
	thing := objectModel("Core.Thing", prop{"image", anyOf(
		inline(&openapi3.Schema{Type: strType("string")}),
		resolvedRef("Images.ImageSpec", foreign),
	)})

	models := []Model{
		{SchemaName: "Images.ImageSpec", Schema: foreign, Namespace: "Images"},
		{SchemaName: thing.SchemaName, Schema: thing.Schema, Namespace: "Core"},
	}
	tf := generating(unionFuncs(models...), map[string]string{"current_package": "core"})
	tf.parser.Flatten(*tf.models, "strip")

	unions := tf.goUnions()

	union := unionByName(t, unions, "ImageUnion")
	if got, want := unions.Fields["Thing.image"], "ImageUnion"; got != want {
		t.Fatalf("Thing.image: got %q, want %q", got, want)
	}
	if got, want := union.TypeNames, []string{"ImageReference", "ImageSpecVariant"}; !equal(got, want) {
		t.Fatalf("variants: got %v, want %v", got, want)
	}
	wrapper := union.Variants[1]
	if !wrapper.Declare || wrapper.Underlying != "ImageSpec" {
		t.Fatalf("wrapper variant: got %+v", wrapper)
	}
	if got, want := wrapper.Namespace, "Images"; got != want {
		t.Fatalf("wrapper variant namespace: got %q, want %q", got, want)
	}
}

// Flattening is idempotent, so a second pass reads names it already stripped the
// namespace from. It has to keep what the first recorded: a schema that has
// forgotten which namespace it came from cannot be qualified, and its union goes
// from wrapped to not generated at all.
func TestGoUnionsVariantKeepsNamespaceAfterRepeatedFlatten(t *testing.T) {
	foreign := imageSpec().Schema
	thing := objectModel("Core.Thing", prop{"image", anyOf(
		inline(&openapi3.Schema{Type: strType("string")}),
		resolvedRef("Images.ImageSpec", foreign),
	)})

	models := []Model{
		{SchemaName: "Images.ImageSpec", Schema: foreign, Namespace: "Images"},
		{SchemaName: thing.SchemaName, Schema: thing.Schema, Namespace: "Core"},
	}
	tf := generating(unionFuncs(models...), map[string]string{"current_package": "core"})
	tf.parser.Flatten(*tf.models, "strip")
	tf.parser.Flatten(*tf.models, "strip")

	if got, want := tf.parser.schemaNamespaceOf("ImageSpec"), "Images"; got != want {
		t.Fatalf("ImageSpec namespace: got %q, want %q", got, want)
	}

	union := unionByName(t, tf.goUnions(), "ImageUnion")

	if got, want := union.TypeNames, []string{"ImageReference", "ImageSpecVariant"}; !equal(got, want) {
		t.Fatalf("variants: got %v, want %v", got, want)
	}
	if got, want := union.Variants[1].Namespace, "Images"; got != want {
		t.Fatalf("wrapper variant namespace: got %q, want %q", got, want)
	}
}

// A `title` on an inline branch names no generated type — hoisting reaches the
// schemas nested in a property, not those nested in its `anyOf` — so the variant
// is declared in terms of the type the branch's shape encodes to.
func TestGoUnionsTitledInlineBranchDeclared(t *testing.T) {
	tf := unionFuncs(
		imageSpec(),
		objectModel("Thing", prop{"image", anyOf(
			inline(&openapi3.Schema{Type: strType("string"), Title: "ImageName"}),
			ref("ImageSpec"),
		)}),
	)

	union := unionByName(t, tf.goUnions(), "ImageUnion")

	declared := union.Variants[0]
	if declared.Type != "ImageReference" || declared.Underlying != "string" || !declared.Declare {
		t.Fatalf("declared variant: got %+v", declared)
	}
}

// A title that happens to match a component names that component no more than
// any other title does: the branch is still one of ours to declare.
func TestGoUnionsInlineTitleDoesNotAdoptComponent(t *testing.T) {
	tf := unionFuncs(
		imageSpec(),
		fooSpec(),
		objectModel("Thing", prop{"image", anyOf(
			inline(&openapi3.Schema{Type: strType("string"), Title: "FooSpec"}),
			ref("ImageSpec"),
		)}),
	)

	union := unionByName(t, tf.goUnions(), "ImageUnion")

	if got, want := union.TypeNames, []string{"ImageReference", "ImageSpec"}; !equal(got, want) {
		t.Fatalf("variants: got %v, want %v", got, want)
	}
	declared := union.Variants[0]
	if !declared.Declare || declared.Underlying != "string" {
		t.Fatalf("declared variant: got %+v", declared)
	}
}

// The allOf-wrapped "$ref plus description" form names a schema just as a bare
// $ref does.
func TestGoUnionsAllOfWrappedRefIsNamed(t *testing.T) {
	tf := unionFuncs(
		imageSpec(),
		objectModel("Thing", prop{"image", anyOf(
			inline(&openapi3.Schema{Type: strType("string")}),
			inline(&openapi3.Schema{
				Description: "The image to run.",
				AllOf:       []*openapi3.SchemaRef{ref("ImageSpec")},
			}),
		)}),
	)

	union := unionByName(t, tf.goUnions(), "ImageUnion")

	named := union.Variants[1]
	if named.Type != "ImageSpec" || named.Declare {
		t.Fatalf("named variant: got %+v", named)
	}
	if got, want := named.Doc, "The image to run."; got != want {
		t.Fatalf("named variant doc: got %q, want %q", got, want)
	}
	if got, want := named.Kinds, []string{`'{'`}; !equal(got, want) {
		t.Fatalf("named variant kinds: got %v, want %v", got, want)
	}
}

// A branch referencing a schema too empty to generate a type of its own is an
// interface{}, which can neither be named nor dispatched on.
func TestGoUnionsSkipsBranchesReferencingEmptySchemas(t *testing.T) {
	tf := unionFuncs(
		objectModel("Thing", prop{"value", anyOf(
			inline(&openapi3.Schema{Type: strType("string")}),
			ref("GoogleProtobufValue"),
		)}),
	)
	tf.parser.doc.Components.Schemas["GoogleProtobufValue"] = &openapi3.SchemaRef{Value: &openapi3.Schema{}}

	unions := tf.goUnions()

	if len(unions.Unions) != 0 {
		t.Fatalf("unions: got %v, want none", unionNames(unions))
	}
	if name, ok := unions.Fields["Thing.value"]; ok {
		t.Fatalf("Thing.value: got %q, want no union", name)
	}
}

// The name a declared variant wants may already belong to a schema of the
// document, which would make the generated package fail to compile.
func TestGoUnionsDeclaredVariantAvoidsExistingSchema(t *testing.T) {
	tf := unionFuncs(
		imageSpec(),
		objectModel("ImageReference", prop{"name", &openapi3.Schema{Type: strType("string")}}),
		objectModel("Thing", prop{"image", anyOf(
			inline(&openapi3.Schema{Type: strType("string")}),
			ref("ImageSpec"),
		)}),
	)

	union := unionByName(t, tf.goUnions(), "ImageUnion")

	if got, want := union.TypeNames, []string{"ImageReference2", "ImageSpec"}; !equal(got, want) {
		t.Fatalf("variants: got %v, want %v", got, want)
	}
}

// A union name that collides with a schema of the document falls back to the
// declaring property rather than redeclaring the schema's name.
func TestGoUnionsNameAvoidsExistingSchema(t *testing.T) {
	tf := unionFuncs(
		imageSpec(),
		objectModel("ImageUnion", prop{"name", &openapi3.Schema{Type: strType("string")}}),
		objectModel("Thing", prop{"image", anyOf(
			inline(&openapi3.Schema{Type: strType("string")}),
			ref("ImageSpec"),
		)}),
	)

	unions := tf.goUnions()

	unionByName(t, unions, "ThingImageUnion")
	if got, want := unions.Fields["Thing.image"], "ThingImageUnion"; got != want {
		t.Fatalf("Thing.image: got %q, want %q", got, want)
	}
}

// The fallback name is not unique either: two property spellings pascal-case to
// the same name, and the second union must not adopt the first one's branches.
func TestGoUnionsFallbackCollisionProbed(t *testing.T) {
	tf := unionFuncs(
		objectModel("Thing",
			prop{"foo-bar", anyOf(
				inline(&openapi3.Schema{Type: strType("string")}),
				inline(&openapi3.Schema{Type: strType("boolean")}),
			)},
			prop{"foo_bar", anyOf(
				inline(&openapi3.Schema{Type: strType("string")}),
				inline(&openapi3.Schema{Type: strType("integer")}),
			)},
		),
	)
	tf.parser.propertyOrders["Thing"] = []string{"foo-bar", "foo_bar"}

	unions := tf.goUnions()

	if got, want := unions.Fields["Thing.foo-bar"], "ThingFooBarUnion"; got != want {
		t.Fatalf("Thing.foo-bar: got %q, want %q", got, want)
	}
	if got, want := unions.Fields["Thing.foo_bar"], "ThingFooBar2Union"; got != want {
		t.Fatalf("Thing.foo_bar: got %q, want %q", got, want)
	}

	first := unionByName(t, unions, "ThingFooBarUnion")
	if got, want := first.TypeNames, []string{"ThingFooBarReference", "ThingFooBarFlag"}; !equal(got, want) {
		t.Fatalf("ThingFooBarUnion variants: got %v, want %v", got, want)
	}
	second := unionByName(t, unions, "ThingFooBar2Union")
	if got, want := second.TypeNames, []string{"ThingFooBar2Reference", "ThingFooBar2Number"}; !equal(got, want) {
		t.Fatalf("ThingFooBar2Union variants: got %v, want %v", got, want)
	}
}

// `anyOf` gives its branches no order, so the same set of shapes is one union
// however the two properties happen to list them.
func TestGoUnionsReorderedBranchesReuseUnion(t *testing.T) {
	tf := unionFuncs(
		imageSpec(),
		objectModel("Thing", prop{"image", anyOf(
			inline(&openapi3.Schema{Type: strType("string")}),
			ref("ImageSpec"),
		)}),
		objectModel("OtherThing", prop{"rom", anyOf(
			ref("ImageSpec"),
			inline(&openapi3.Schema{Type: strType("string")}),
		)}),
	)

	unions := tf.goUnions()

	if got, want := len(unions.Unions), 1; got != want {
		t.Fatalf("unions: got %v, want %d", unionNames(unions), want)
	}
	if got, want := unions.Fields["Thing.image"], "ImageUnion"; got != want {
		t.Fatalf("Thing.image: got %q, want %q", got, want)
	}
	if got, want := unions.Fields["OtherThing.rom"], "ImageUnion"; got != want {
		t.Fatalf("OtherThing.rom: got %q, want %q", got, want)
	}
}

// A 3.0 branch spells the null half of its type `nullable`. Null is decoded by
// the union rather than dispatched to a variant, so the branch keeps the kind of
// the value it holds and the union is generated as it would be without it.
func TestGoUnionsNullableBranchKeepsValueKind(t *testing.T) {
	tf := unionFuncs(
		imageSpec(),
		objectModel("Thing", prop{"image", anyOf(
			inline(&openapi3.Schema{Type: strType("string"), Nullable: true}),
			ref("ImageSpec"),
		)}),
	)

	union := unionByName(t, tf.goUnions(), "ImageUnion")

	if got, want := union.Variants[0].Underlying, "string"; got != want {
		t.Fatalf("nullable variant underlying: got %q, want %q", got, want)
	}
	if got, want := union.Variants[0].Kinds, []string{`'"'`}; !equal(got, want) {
		t.Fatalf("nullable variant kinds: got %v, want %v", got, want)
	}
}

// A 3.1 branch lists null among its types instead. It is dropped for the same
// reason, rather than left to make the branch a type nothing can encode.
func TestGoUnionsNullInTypeListDropped(t *testing.T) {
	tf := unionFuncs(
		imageSpec(),
		objectModel("Thing", prop{"image", anyOf(
			inline(&openapi3.Schema{Type: &openapi3.Types{"string", "null"}}),
			ref("ImageSpec"),
		)}),
	)

	union := unionByName(t, tf.goUnions(), "ImageUnion")

	if got, want := union.TypeNames, []string{"ImageReference", "ImageSpec"}; !equal(got, want) {
		t.Fatalf("variants: got %v, want %v", got, want)
	}
	if got, want := union.Variants[0].Underlying, "string"; got != want {
		t.Fatalf("first underlying: got %q, want %q", got, want)
	}
	if got, want := union.Variants[0].Kinds, []string{`'"'`}; !equal(got, want) {
		t.Fatalf("first kinds: got %v, want %v", got, want)
	}
}

// A branch of more than one value type still encodes to a single Go type, which
// can hold only one of the shapes it advertises. Dispatching the others to it
// fails to decode, so the property is left as it was.
func TestGoUnionsRejectsMultiTypedBranch(t *testing.T) {
	tf := unionFuncs(
		objectModel("Thing", prop{"image", anyOf(
			inline(&openapi3.Schema{Type: strType("boolean")}),
			ref("Ref"),
		)}),
		namedModel("Ref", &openapi3.Schema{Type: &openapi3.Types{"string", "object"}}),
	)

	if unions := tf.goUnions(); len(unions.Unions) != 0 {
		t.Fatalf("unions: got %v, want none", unionNames(unions))
	}
}

// An inline branch of more than one type is rejected for the same reason, rather
// than described by whichever of the shapes its Go type happens to be.
func TestGoUnionsRejectsMultiTypedInlineBranch(t *testing.T) {
	tf := unionFuncs(
		imageSpec(),
		objectModel("Thing", prop{"image", anyOf(
			inline(&openapi3.Schema{Type: &openapi3.Types{"string", "array"}}),
			ref("ImageSpec"),
		)}),
	)

	if unions := tf.goUnions(); len(unions.Unions) != 0 {
		t.Fatalf("unions: got %v, want none", unionNames(unions))
	}
}

// An inline branch whose Go type names a schema of this package is fine: the
// item type is declared alongside the union.
func TestGoUnionsInlineListOfLocalRef(t *testing.T) {
	tf := unionFuncs(
		imageSpec(),
		fooSpec(),
		objectModel("Thing", prop{"image", anyOf(
			inline(&openapi3.Schema{Type: strType("array"), Items: ref("FooSpec")}),
			ref("ImageSpec"),
		)}),
	)

	union := unionByName(t, tf.goUnions(), "ImageUnion")

	if got, want := union.Variants[0].Underlying, "[]FooSpec"; got != want {
		t.Fatalf("list underlying: got %q, want %q", got, want)
	}
}

// A variant qualifies the branch as a whole, not a type nested inside it, so an
// inline shape reaching a schema of another package is left alone rather than
// described by a type name that does not resolve in the generated package.
func TestGoUnionsSkipsInlineListOfForeignRef(t *testing.T) {
	foreign := withPackage(objectModel("FooSpec", prop{"foo", &openapi3.Schema{Type: strType("string")}}).Schema, "other")

	tf := generating(unionFuncs(
		imageSpec(),
		namedModel("FooSpec", foreign),
		objectModel("Thing", prop{"image", anyOf(
			inline(&openapi3.Schema{Type: strType("array"), Items: resolvedRef("FooSpec", foreign)}),
			ref("ImageSpec"),
		)}),
	), map[string]string{"x-package": "mine"})

	if unions := tf.goUnions(); len(unions.Unions) != 0 {
		t.Fatalf("unions: got %v, want none", unionNames(unions))
	}
}

// The same holds for a map value referencing a schema that filtering left out of
// the run altogether: nothing declares the type it would be described by.
func TestGoUnionsSkipsInlineMapOfFilteredRef(t *testing.T) {
	tf := unionFuncs(
		scalarModel("ImageSpec", "string"),
		objectModel("Thing", prop{"image", anyOf(
			inline(&openapi3.Schema{
				Type: strType("object"),
				AdditionalProperties: openapi3.AdditionalProperties{
					Schema: ref("Gone"),
				},
			}),
			ref("ImageSpec"),
		)}),
	)
	// Gone is a component of the document that this run does not generate.
	tf.parser.doc.Components.Schemas["Gone"] = &openapi3.SchemaRef{
		Value: &openapi3.Schema{Type: strType("object"), Properties: openapi3.Schemas{
			"gone": inline(&openapi3.Schema{Type: strType("string")}),
		}},
	}

	if unions := tf.goUnions(); len(unions.Unions) != 0 {
		t.Fatalf("unions: got %v, want none", unionNames(unions))
	}
}

// A branch overlapping another on only one of its kinds is still ambiguous.
func TestGoUnionsRejectsPartiallyOverlappingKinds(t *testing.T) {
	tf := unionFuncs(
		objectModel("Thing", prop{"image", anyOf(
			inline(&openapi3.Schema{Type: strType("string")}),
			ref("Ref"),
		)}),
		namedModel("Ref", &openapi3.Schema{Type: &openapi3.Types{"string", "object"}}),
	)

	if unions := tf.goUnions(); len(unions.Unions) != 0 {
		t.Fatalf("unions: got %v, want none", unionNames(unions))
	}
}

// An enum written without a type is typed by its values. Calling it a string
// would have the decoder hand JSON numbers to a string-backed variant.
func TestGoUnionsTypelessNumericEnumIsNumeric(t *testing.T) {
	tf := unionFuncs(
		imageSpec(),
		objectModel("Thing", prop{"level", anyOf(
			inline(&openapi3.Schema{Enum: []any{1, 2}}),
			ref("ImageSpec"),
		)}),
	)

	union := unionByName(t, tf.goUnions(), "ImageUnion")

	if got, want := union.TypeNames, []string{"ImageNumber", "ImageSpec"}; !equal(got, want) {
		t.Fatalf("variants: got %v, want %v", got, want)
	}
	if got, want := union.Variants[0].Underlying, "int"; got != want {
		t.Fatalf("enum underlying: got %q, want %q", got, want)
	}
	if got, want := union.Variants[0].Kinds, []string{`'0'`}; !equal(got, want) {
		t.Fatalf("enum kinds: got %v, want %v", got, want)
	}
}

// A named branch that composes a scalar is of that scalar's kind. Assuming an
// object of every named branch without a written type dispatches JSON strings to
// a struct.
func TestGoUnionsNamedScalarCompositionKeepsScalarKind(t *testing.T) {
	tf := unionFuncs(
		imageSpec(),
		objectModel("Thing", prop{"image", anyOf(
			ref("Name"),
			ref("ImageSpec"),
		)}),
		namedModel("Name", &openapi3.Schema{
			AllOf: []*openapi3.SchemaRef{inline(&openapi3.Schema{Type: strType("string")})},
		}),
	)

	union := unionByName(t, tf.goUnions(), "NameUnion")

	if got, want := union.Variants[0].Kinds, []string{`'"'`}; !equal(got, want) {
		t.Fatalf("composed scalar kinds: got %v, want %v", got, want)
	}
}

// `type: object` is routinely left off a schema that describes one, which is
// still an object branch.
func TestGoUnionsTypelessObjectIsObject(t *testing.T) {
	tf := unionFuncs(
		objectModel("Thing", prop{"image", anyOf(
			inline(&openapi3.Schema{Type: strType("string")}),
			ref("Ref"),
		)}),
		namedModel("Ref", &openapi3.Schema{Properties: openapi3.Schemas{
			"name": inline(&openapi3.Schema{Type: strType("string")}),
		}}),
	)

	union := unionByName(t, tf.goUnions(), "RefUnion")

	if got, want := union.Variants[1].Kinds, []string{`'{'`}; !equal(got, want) {
		t.Fatalf("typeless object kinds: got %v, want %v", got, want)
	}
}

// A named branch whose type cannot be derived is not assumed to be an object:
// dispatching the wrong kind to it decodes a valid payload into the wrong
// variant, which the property is better left as it was than have.
func TestGoUnionsSkipsNamedBranchOfUnknownType(t *testing.T) {
	tf := unionFuncs(
		imageSpec(),
		objectModel("Thing", prop{"image", anyOf(
			ref("Opaque"),
			ref("ImageSpec"),
		)}),
		namedModel("Opaque", &openapi3.Schema{
			OneOf: []*openapi3.SchemaRef{inline(&openapi3.Schema{})},
		}),
	)

	if unions := tf.goUnions(); len(unions.Unions) != 0 {
		t.Fatalf("unions: got %v, want none", unionNames(unions))
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
