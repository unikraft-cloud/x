// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"sort"

	"github.com/ettle/strcase"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/mitchellh/copystructure"
)

// preprocessor handles extracting inline schemas from OpenAPI spec
type preprocessor struct {
	doc            *openapi3.T
	parser         *Parser
	processed      map[string]bool // track processed schemas to avoid duplicates
	createdSchemas map[string]bool // track schemas we created (for cleanup)
}

// Preprocess extracts inline schemas from the OpenAPI spec
// This mimics the behavior of OpenAPI Generator's InlineModelResolver
func Preprocess(doc *openapi3.T, parser *Parser) {
	p := &preprocessor{
		doc:            doc,
		parser:         parser,
		processed:      make(map[string]bool),
		createdSchemas: make(map[string]bool),
	}

	// Collect original schema names first to avoid processing inline schemas we create
	originalSchemas := make([]string, 0, len(p.doc.Components.Schemas))
	for name := range p.doc.Components.Schemas {
		originalSchemas = append(originalSchemas, name)
	}
	sort.Strings(originalSchemas)

	// Process each original schema
	for _, name := range originalSchemas {
		schemaRef := p.doc.Components.Schemas[name]
		if schemaRef.Value == nil {
			continue
		}
		p.gatherInlineModels(schemaRef.Value, name)
	}

	// Process inline request/response body schemas in operations
	p.processOperationSchemas()

	// Clean up orphaned schemas
	p.removeOrphanedSchemas()
}

// gatherInlineModels recursively extracts inline models from a schema
// This is based on InlineModelResolver.gatherInlineModels()
func (p *preprocessor) gatherInlineModels(schema *openapi3.Schema, modelPrefix string) {
	if schema == nil {
		return
	}

	// Prevent processing the same schema twice
	if p.processed[modelPrefix] {
		return
	}
	p.processed[modelPrefix] = true

	// Process properties only - this is where inline schemas are created
	if schema.Type == nil || schema.Type.Is("object") {
		p.processProperties(schema, modelPrefix)
	}
}

// processProperties processes object properties for inline models
func (p *preprocessor) processProperties(schema *openapi3.Schema, modelPrefix string) {
	// Collect properties to process first (to avoid map iteration issues)
	type propToProcess struct {
		parentSchema *openapi3.Schema
		name         string
		ref          *openapi3.SchemaRef
	}
	var propsToProcess []propToProcess

	// Collect direct properties
	for propName, propRef := range schema.Properties {
		propsToProcess = append(propsToProcess, propToProcess{
			parentSchema: schema,
			name:         propName,
			ref:          propRef,
		})
	}

	// Also collect properties found in allOf schemas
	// This handles cases like AttachVolumesRequest where properties are in allOf[0].properties
	for _, allOfRef := range schema.AllOf {
		if allOfRef.Value == nil {
			continue
		}
		for propName, propRef := range allOfRef.Value.Properties {
			propsToProcess = append(propsToProcess, propToProcess{
				parentSchema: allOfRef.Value,
				name:         propName,
				ref:          propRef,
			})
		}
	}

	// Now process all collected properties
	for _, prop := range propsToProcess {
		inlineName := p.inlineTypeName(modelPrefix, prop.name)
		p.setInlineTypeName(prop.ref, inlineName)
		fromAllOf := prop.parentSchema != schema
		p.processProperty(prop.parentSchema, prop.name, prop.ref, inlineName, fromAllOf)
	}
}

func (p *preprocessor) inlineTypeName(modelPrefix, propName string) string {
	return modelPrefix + strcase.ToPascal(propName)
}

// setInlineTypeName ensures inline schemas have a name for Go types.
func (p *preprocessor) setInlineTypeName(propRef *openapi3.SchemaRef, inlineName string) {
	if propRef == nil || propRef.Value == nil {
		return
	}
	propSchema := propRef.Value
	if len(propSchema.Enum) > 0 {
		propSchema.Title = inlineName
	}
	if propSchema.Type != nil && propSchema.Type.Is("array") && propSchema.Items != nil && propSchema.Items.Value != nil {
		itemSchema := propSchema.Items.Value
		if len(itemSchema.Enum) > 0 {
			itemSchema.Title = inlineName
		}
		if itemSchema.Type != nil && itemSchema.Type.Is("object") && len(itemSchema.Properties) > 0 {
			itemSchema.Title = inlineName
		}
	}
	if propSchema.Type != nil && propSchema.Type.Is("object") && len(propSchema.Properties) > 0 {
		propSchema.Title = inlineName
	}
}

// setInlineSchemaTypeNames updates inline enum/object type names for a schema.
func (p *preprocessor) setInlineSchemaTypeNames(schema *openapi3.Schema, modelPrefix string) {
	if schema == nil {
		return
	}
	for propName, propRef := range schema.Properties {
		inlineName := p.inlineTypeName(modelPrefix, propName)
		p.setInlineTypeName(propRef, inlineName)
	}
	for _, allOfRef := range schema.AllOf {
		if allOfRef.Value == nil {
			continue
		}
		for propName, propRef := range allOfRef.Value.Properties {
			inlineName := p.inlineTypeName(modelPrefix, propName)
			p.setInlineTypeName(propRef, inlineName)
		}
	}
}

func (p *preprocessor) cloneSchema(schema *openapi3.Schema) *openapi3.Schema {
	if schema == nil {
		return nil
	}
	clone, err := copystructure.Copy(schema)
	if err != nil {
		panic(err)
	}
	return clone.(*openapi3.Schema)
}

// processProperty processes a single property for inline model extraction
func (p *preprocessor) processProperty(parentSchema *openapi3.Schema, propName string, propRef *openapi3.SchemaRef, schemaName string, fromAllOf bool) {
	if propRef.Value == nil {
		return
	}

	prop := propRef.Value

	// ONLY process properties with allOf + single $ref
	// This is the pattern that needs inline schema extraction
	if len(prop.AllOf) != 1 || prop.AllOf[0].Ref == "" {
		return
	}

	// Get the referenced schema
	refSchemaName := extractTypeFromRef(prop.AllOf[0].Ref)
	refSchemaRef := p.doc.Components.Schemas[refSchemaName]
	if refSchemaRef == nil || refSchemaRef.Value == nil {
		return
	}
	refSchema := refSchemaRef.Value

	// Skip if the referenced schema has no type and no properties (like GoogleProtobufValue)
	// These should be mapped to interface{}
	if refSchema.Type == nil && len(refSchema.Properties) == 0 && len(refSchema.AllOf) == 0 {
		parentSchema.Properties[propName] = wrapRefWithDescription(prop.AllOf[0].Ref, prop.Description)
		return
	}

	// Skip if the referenced schema is just an enum (not an object)
	// Enums should use the direct reference, not create inline schemas
	if len(refSchema.Enum) > 0 && len(refSchema.Properties) == 0 {
		parentSchema.Properties[propName] = wrapRefWithDescription(prop.AllOf[0].Ref, prop.Description)
		return
	}

	// Check if the referenced schema is an object with properties
	if p.isModelNeeded(refSchema) {
		// IMPORTANT: Only create inline schemas for DIRECT properties, not for properties in allOf
		// Properties in allOf should use the direct reference with description preserved on the field
		if fromAllOf {
			parentSchema.Properties[propName] = wrapRefWithDescription(prop.AllOf[0].Ref, prop.Description)
			return
		}

		// Create an inline schema for direct properties
		// Use the property's description as the type description for the inline schema
		inlineSchema := p.cloneSchema(refSchema)
		inlineSchema.Description = prop.Description
		inlineSchema.Extensions = map[string]any{"x-inline-schema": true}

		p.setInlineSchemaTypeNames(inlineSchema, schemaName)

		// Add to components/schemas
		p.doc.Components.Schemas[schemaName] = &openapi3.SchemaRef{
			Value: inlineSchema,
		}

		// Track that we created this schema
		p.createdSchemas[schemaName] = true

		// Register property order for the inline schema
		// Use the order from the referenced schema
		if refOrder := p.parser.GetPropertyOrder(refSchemaName); len(refOrder) > 0 {
			p.parser.SetPropertyOrder(schemaName, refOrder)
		}

		// Replace the property with a ref to the new schema
		parentSchema.Properties[propName] = &openapi3.SchemaRef{
			Ref: "#/components/schemas/" + schemaName,
		}

		// Don't recurse into the referenced schema - it will be processed independently
		// if it's a top-level schema in components/schemas. This ensures each top-level
		// schema creates its own inline types (e.g., CreateInstanceRequestScaleToZero and
		// InstanceScaleToZero are both created from InstanceScaleToZero reference).
	} else {
		// For non-objects (enums, simple types), replace with the ref directly
		parentSchema.Properties[propName] = &openapi3.SchemaRef{
			Ref: prop.AllOf[0].Ref,
		}
	}
}

// isModelNeeded determines if a schema should be extracted as a separate model
// Based on InlineModelResolver.isModelNeeded()
func (p *preprocessor) isModelNeeded(schema *openapi3.Schema) bool {
	if schema == nil {
		return false
	}

	// Pure enums (without properties) should NOT be extracted as inline schemas
	// They should use the direct reference
	if len(schema.Enum) > 0 && len(schema.Properties) == 0 {
		// Check if it also doesn't have allOf/oneOf with properties
		hasNestedProperties := false
		for _, allOfRef := range schema.AllOf {
			if allOfRef.Value != nil && len(allOfRef.Value.Properties) > 0 {
				hasNestedProperties = true
				break
			}
		}
		if !hasNestedProperties {
			return false
		}
	}

	// Objects with properties should be extracted
	if (schema.Type == nil || schema.Type.Is("object")) && len(schema.Properties) > 0 {
		return true
	}

	// Check if there are properties in allOf/oneOf
	for _, allOfRef := range schema.AllOf {
		if allOfRef.Value == nil {
			continue
		}
		if len(allOfRef.Value.Properties) > 0 {
			return true
		}
		// Check oneOf within allOf
		for _, oneOfRef := range allOfRef.Value.OneOf {
			if oneOfRef.Value != nil && len(oneOfRef.Value.Properties) > 0 {
				return true
			}
		}
	}

	return false
}

// removeOrphanedSchemas removes schemas that were created during preprocessing
// but are never referenced
func (p *preprocessor) removeOrphanedSchemas() {
	// Collect all schema references
	referenced := make(map[string]bool)

	// Scan all schemas to find references
	for _, schemaRef := range p.doc.Components.Schemas {
		if schemaRef.Value != nil {
			p.collectReferences(schemaRef.Value, referenced)
		}
	}

	// Also scan operation request/response bodies for references
	for _, pathItem := range p.doc.Paths.Map() {
		ops := []*openapi3.Operation{
			pathItem.Get,
			pathItem.Post,
			pathItem.Put,
			pathItem.Delete,
			pathItem.Patch,
		}

		for _, op := range ops {
			if op == nil {
				continue
			}

			// Check request body
			if op.RequestBody != nil && op.RequestBody.Value != nil {
				for _, content := range op.RequestBody.Value.Content {
					if content.Schema != nil {
						if content.Schema.Ref != "" {
							refName := extractTypeFromRef(content.Schema.Ref)
							referenced[refName] = true
						}
						if content.Schema.Value != nil {
							p.collectReferences(content.Schema.Value, referenced)
						}
					}
				}
			}

			// Check responses
			if op.Responses != nil {
				for _, respRef := range op.Responses.Map() {
					if respRef == nil || respRef.Value == nil {
						continue
					}
					for _, content := range respRef.Value.Content {
						if content.Schema != nil {
							if content.Schema.Ref != "" {
								refName := extractTypeFromRef(content.Schema.Ref)
								referenced[refName] = true
							}
							if content.Schema.Value != nil {
								p.collectReferences(content.Schema.Value, referenced)
							}
						}
					}
				}
			}
		}
	}

	// Remove created schemas that are not referenced
	for schemaName := range p.createdSchemas {
		if referenced[schemaName] {
			continue
		}
		delete(p.doc.Components.Schemas, schemaName)
		delete(p.parser.propertyOrders, schemaName)
	}
}

// collectReferences recursively collects all schema references from a schema
func (p *preprocessor) collectReferences(schema *openapi3.Schema, referenced map[string]bool) {
	if schema == nil {
		return
	}

	// Check properties
	for _, propRef := range schema.Properties {
		if propRef.Ref != "" {
			refName := extractTypeFromRef(propRef.Ref)
			referenced[refName] = true
		}
		if propRef.Value != nil {
			p.collectReferences(propRef.Value, referenced)
		}
	}

	// Check allOf
	for _, allOfRef := range schema.AllOf {
		if allOfRef.Ref != "" {
			refName := extractTypeFromRef(allOfRef.Ref)
			referenced[refName] = true
		}
		if allOfRef.Value != nil {
			p.collectReferences(allOfRef.Value, referenced)
		}
	}

	// Check oneOf
	for _, oneOfRef := range schema.OneOf {
		if oneOfRef.Ref != "" {
			refName := extractTypeFromRef(oneOfRef.Ref)
			referenced[refName] = true
		}
		if oneOfRef.Value != nil {
			p.collectReferences(oneOfRef.Value, referenced)
		}
	}

	// Check anyOf
	for _, anyOfRef := range schema.AnyOf {
		if anyOfRef.Ref != "" {
			refName := extractTypeFromRef(anyOfRef.Ref)
			referenced[refName] = true
		}
		if anyOfRef.Value != nil {
			p.collectReferences(anyOfRef.Value, referenced)
		}
	}

	// Check items (for arrays)
	if schema.Items != nil {
		if schema.Items.Ref != "" {
			refName := extractTypeFromRef(schema.Items.Ref)
			referenced[refName] = true
		}
		if schema.Items.Value != nil {
			p.collectReferences(schema.Items.Value, referenced)
		}
	}

	// Check additionalProperties
	if schema.AdditionalProperties.Schema != nil {
		if schema.AdditionalProperties.Schema.Ref != "" {
			refName := extractTypeFromRef(schema.AdditionalProperties.Schema.Ref)
			referenced[refName] = true
		}
		if schema.AdditionalProperties.Schema.Value != nil {
			p.collectReferences(schema.AdditionalProperties.Schema.Value, referenced)
		}
	}
}

// processOperationSchemas extracts inline schemas from request/response bodies
func (p *preprocessor) processOperationSchemas() {
	for _, pathItem := range p.doc.Paths.Map() {
		ops := []*openapi3.Operation{
			pathItem.Get,
			pathItem.Post,
			pathItem.Put,
			pathItem.Delete,
			pathItem.Patch,
		}

		for _, op := range ops {
			if op == nil || op.OperationID == "" {
				continue
			}

			// Process request body
			if op.RequestBody != nil && op.RequestBody.Value != nil {
				content := op.RequestBody.Value.Content.Get("application/json")
				if content != nil && content.Schema != nil {
					p.processInlineSchema(content.Schema, op.OperationID, "Request")
				}

				// Handle binary uploads - create a simple schema with type: string, format: binary
				// This will be mapped to []byte or io.Reader in the Go code
				binaryContent := op.RequestBody.Value.Content.Get("application/octet-stream")
				if binaryContent != nil && binaryContent.Schema != nil && binaryContent.Schema.Value != nil {
					// Don't create a separate schema for binary - just ensure it has a proper type
					// The template will handle binary specially
					if binaryContent.Schema.Value.Type == nil || !binaryContent.Schema.Value.Type.Is("string") {
						binaryContent.Schema.Value.Type = &openapi3.Types{"string"}
					}
					if binaryContent.Schema.Value.Format == "" {
						binaryContent.Schema.Value.Format = "binary"
					}
				}
			}

			// Process response bodies
			if op.Responses != nil {
				defaultResp := op.Responses.Default()
				if defaultResp != nil && defaultResp.Value != nil {
					respContent := defaultResp.Value.Content.Get("application/json")
					if respContent != nil && respContent.Schema != nil {
						p.processInlineSchema(respContent.Schema, op.OperationID, "Response")
					}
				}
			}
		}
	}
}

// processInlineSchema converts an inline schema to a ref by creating a new schema
func (p *preprocessor) processInlineSchema(schemaRef *openapi3.SchemaRef, opID, suffix string) {
	if schemaRef == nil || schemaRef.Value == nil {
		return
	}

	// Skip if it's already a ref
	if schemaRef.Ref != "" {
		return
	}

	schema := schemaRef.Value

	// Only process inline object schemas with properties or arrays of objects
	if schema.Type == nil {
		return
	}

	if schema.Type.Is("object") && len(schema.Properties) > 0 {
		// Create a new schema name based on the operation ID
		schemaName := opID + suffix

		// Clone the schema
		inlineSchema := p.cloneSchema(schema)
		p.setInlineSchemaTypeNames(inlineSchema, schemaName)

		// Add to components/schemas
		p.doc.Components.Schemas[schemaName] = &openapi3.SchemaRef{
			Value: inlineSchema,
		}

		// Track that we created this schema
		p.createdSchemas[schemaName] = true

		// Extract property order from the inline schema
		propOrder := make([]string, 0, len(schema.Properties))
		for propName := range schema.Properties {
			propOrder = append(propOrder, propName)
		}
		if len(propOrder) > 0 {
			p.parser.SetPropertyOrder(schemaName, propOrder)
		}

		// Replace with a ref
		schemaRef.Ref = "#/components/schemas/" + schemaName
		schemaRef.Value = nil
	} else if schema.Type.Is("array") && schema.Items != nil && schema.Items.Value != nil {
		// Handle inline array item schemas
		itemSchema := schema.Items.Value
		if itemSchema.Type != nil && itemSchema.Type.Is("object") && len(itemSchema.Properties) > 0 {
			// Create a new schema name for the array items
			schemaName := opID + suffix + "Item"

			// Clone the item schema
			inlineSchema := p.cloneSchema(itemSchema)
			p.setInlineSchemaTypeNames(inlineSchema, schemaName)

			// Add to components/schemas
			p.doc.Components.Schemas[schemaName] = &openapi3.SchemaRef{
				Value: inlineSchema,
			}

			// Track that we created this schema
			p.createdSchemas[schemaName] = true

			// Extract property order
			propOrder := make([]string, 0, len(itemSchema.Properties))
			for propName := range itemSchema.Properties {
				propOrder = append(propOrder, propName)
			}
			if len(propOrder) > 0 {
				p.parser.SetPropertyOrder(schemaName, propOrder)
			}

			// Replace items with a ref
			schema.Items.Ref = "#/components/schemas/" + schemaName
			schema.Items.Value = nil
		}
	}
}

// wrapRefWithDescription creates a schema that wraps a $ref while preserving description
func wrapRefWithDescription(ref string, description string) *openapi3.SchemaRef {
	return &openapi3.SchemaRef{
		Value: &openapi3.Schema{
			AllOf: []*openapi3.SchemaRef{
				{Ref: ref},
			},
			Description: description,
		},
	}
}
