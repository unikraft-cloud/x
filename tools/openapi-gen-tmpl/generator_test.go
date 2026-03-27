// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Sample OpenAPI 3.0 specification for testing
const openAPI3Spec = `
openapi: "3.0.3"
info:
  title: Pet Store API
  description: A sample API for testing code generation
  version: "1.0.0"
  contact:
    name: API Support
    email: support@example.com
  license:
    name: MIT
    url: https://opensource.org/licenses/MIT
servers:
  - url: https://api.example.com/v1
    description: Production server
  - url: https://staging-api.example.com/v1
    description: Staging server
tags:
  - name: pets
    description: Operations about pets
  - name: users
    description: Operations about users
paths:
  /pets:
    get:
      operationId: listPets
      summary: List all pets
      description: Returns a list of all pets in the store
      tags:
        - pets
      parameters:
        - name: limit
          in: query
          description: Maximum number of pets to return
          required: false
          schema:
            type: integer
            format: int32
            minimum: 1
            maximum: 100
        - name: status
          in: query
          description: Filter by status
          schema:
            type: string
            enum:
              - available
              - pending
              - sold
      responses:
        "200":
          description: A list of pets
          content:
            application/json:
              schema:
                type: array
                items:
                  $ref: "#/components/schemas/Pet"
        "400":
          description: Invalid request
    post:
      operationId: createPet
      summary: Create a pet
      tags:
        - pets
      requestBody:
        description: Pet to create
        required: true
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/CreatePetRequest"
      responses:
        "201":
          description: Pet created
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Pet"
  /pets/{petId}:
    get:
      operationId: getPet
      summary: Get a pet by ID
      tags:
        - pets
      parameters:
        - name: petId
          in: path
          required: true
          description: The ID of the pet
          schema:
            type: string
            format: uuid
      responses:
        "200":
          description: A pet
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Pet"
        "404":
          description: Pet not found
    delete:
      operationId: deletePet
      summary: Delete a pet
      tags:
        - pets
      parameters:
        - name: petId
          in: path
          required: true
          schema:
            type: string
      responses:
        "204":
          description: Pet deleted
      security:
        - bearerAuth:
            - write:pets
components:
  schemas:
    Pet:
      type: object
      required:
        - id
        - name
      properties:
        id:
          type: string
          format: uuid
          description: Unique identifier
        name:
          type: string
          description: Pet's name
          minLength: 1
          maxLength: 100
        status:
          type: string
          enum:
            - available
            - pending
            - sold
        tags:
          type: array
          items:
            type: string
        metadata:
          type: object
          additionalProperties:
            type: string
    CreatePetRequest:
      type: object
      required:
        - name
      properties:
        name:
          type: string
        status:
          type: string
          default: available
    Error:
      type: object
      properties:
        code:
          type: integer
        message:
          type: string
  securitySchemes:
    bearerAuth:
      type: http
      scheme: bearer
      bearerFormat: JWT
    apiKey:
      type: apiKey
      name: X-API-Key
      in: header
`

// Sample Swagger 2.0 specification for testing
const swagger2Spec = `
swagger: "2.0"
info:
  title: Legacy Pet Store API
  description: A legacy Swagger 2.0 API
  version: "1.0.0"
host: api.example.com
basePath: /v1
schemes:
  - https
consumes:
  - application/json
produces:
  - application/json
tags:
  - name: pets
    description: Pet operations
paths:
  /pets:
    get:
      operationId: listPets
      summary: List pets
      tags:
        - pets
      parameters:
        - name: limit
          in: query
          type: integer
          format: int32
      responses:
        200:
          description: Pet list
          schema:
            type: array
            items:
              $ref: "#/definitions/Pet"
    post:
      operationId: createPet
      summary: Create pet
      tags:
        - pets
      parameters:
        - name: body
          in: body
          required: true
          schema:
            $ref: "#/definitions/Pet"
      responses:
        201:
          description: Created
definitions:
  Pet:
    type: object
    required:
      - name
    properties:
      id:
        type: string
      name:
        type: string
      status:
        type: string
        enum:
          - available
          - pending
          - sold
securityDefinitions:
  api_key:
    type: apiKey
    name: api_key
    in: header
  petstore_auth:
    type: oauth2
    authorizationUrl: https://example.com/oauth/authorize
    flow: implicit
    scopes:
      write:pets: modify pets
      read:pets: read pets
`

// Simple markdown documentation template
const markdownTemplate = `# {{ .Info.Title }}

{{ .Info.Description }}

**Version:** {{ .Info.Version }}
`

// Template for generating a Go client
const goClientTemplate = `// Code generated by openapi-gen-tmpl. DO NOT EDIT.
// API: {{ .Info.Title }}
// Version: {{ .Info.Version }}

package client

const (
	DefaultBaseURL = "{{ with index .Servers 0 }}{{ .URL }}{{ end }}"
)

{{- range .Schemas }}

type {{ pascalCase .Name }} struct {
{{- range .Properties }}
	{{ pascalCase .Name }} {{ goType .Schema }}
{{- end }}
}
{{- end }}
`

// Template for generating TypeScript types
const tsTypesTemplate = `// Code generated by openapi-gen-tmpl. DO NOT EDIT.
// API: {{ .Info.Title }}

{{- range .Schemas }}

export interface {{ pascalCase .Name }} {
{{- range .Properties }}
  {{ camelCase .Name }}{{ if not .Required }}?{{ end }}: {{ typeScriptType .Schema }};
{{- end }}
}
{{- end }}
`

func TestNewGenerator_OpenAPI3(t *testing.T) {
	tmpDir := t.TempDir()
	templatesDir := filepath.Join(tmpDir, "templates")
	outputDir := filepath.Join(tmpDir, "output")

	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		t.Fatalf("failed to create templates dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(templatesDir, "api.md.tmpl"), []byte(markdownTemplate), 0o644); err != nil {
		t.Fatalf("failed to write template: %v", err)
	}

	gen, err := NewGenerator(templatesDir, []byte(openAPI3Spec), outputDir)
	if err != nil {
		t.Fatalf("failed to create generator: %v", err)
	}

	if gen.data == nil {
		t.Fatal("expected data to be non-nil")
	}

	if !gen.data.IsOpenAPI3 {
		t.Error("expected IsOpenAPI3 to be true")
	}

	if gen.data.IsSwagger {
		t.Error("expected IsSwagger to be false")
	}

	if gen.data.Info == nil {
		t.Fatal("expected Info to be non-nil")
	}

	if gen.data.Info.Title != "Pet Store API" {
		t.Errorf("expected title 'Pet Store API', got '%s'", gen.data.Info.Title)
	}

	if gen.data.Info.Version != "1.0.0" {
		t.Errorf("expected version '1.0.0', got '%s'", gen.data.Info.Version)
	}

	if len(gen.data.Servers) != 2 {
		t.Errorf("expected 2 servers, got %d", len(gen.data.Servers))
	}

	if len(gen.data.Tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(gen.data.Tags))
	}

	if len(gen.data.Paths) == 0 {
		t.Error("expected paths to be non-empty")
	}

	if len(gen.data.Schemas) == 0 {
		t.Error("expected schemas to be non-empty")
	}

	if len(gen.data.SecuritySchemes) == 0 {
		t.Error("expected security schemes to be non-empty")
	}
}

func TestNewGenerator_Swagger2(t *testing.T) {
	tmpDir := t.TempDir()
	templatesDir := filepath.Join(tmpDir, "templates")
	outputDir := filepath.Join(tmpDir, "output")

	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		t.Fatalf("failed to create templates dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(templatesDir, "api.md.tmpl"), []byte(markdownTemplate), 0o644); err != nil {
		t.Fatalf("failed to write template: %v", err)
	}

	gen, err := NewGenerator(templatesDir, []byte(swagger2Spec), outputDir)
	if err != nil {
		t.Fatalf("failed to create generator: %v", err)
	}

	if gen.data == nil {
		t.Fatal("expected data to be non-nil")
	}

	if gen.data.IsOpenAPI3 {
		t.Error("expected IsOpenAPI3 to be false")
	}

	if !gen.data.IsSwagger {
		t.Error("expected IsSwagger to be true")
	}

	if gen.data.Info.Title != "Legacy Pet Store API" {
		t.Errorf("expected title 'Legacy Pet Store API', got '%s'", gen.data.Info.Title)
	}

	if len(gen.data.Servers) == 0 {
		t.Error("expected servers to be non-empty (constructed from host/basePath)")
	}

	if len(gen.data.Paths) == 0 {
		t.Error("expected paths to be non-empty")
	}

	if len(gen.data.Schemas) == 0 {
		t.Error("expected schemas (definitions) to be non-empty")
	}
}

func TestGenerator_Files(t *testing.T) {
	tmpDir := t.TempDir()
	templatesDir := filepath.Join(tmpDir, "templates")
	outputDir := filepath.Join(tmpDir, "output")

	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		t.Fatalf("failed to create templates dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(templatesDir, "README.md.tmpl"), []byte(markdownTemplate), 0o644); err != nil {
		t.Fatalf("failed to write template: %v", err)
	}

	gen, err := NewGenerator(templatesDir, []byte(openAPI3Spec), outputDir)
	if err != nil {
		t.Fatalf("failed to create generator: %v", err)
	}

	files := gen.Files(context.Background())

	if len(files) == 0 {
		t.Fatal("expected at least one generated file")
	}

	var readmeFile *GeneratedFile
	for i := range files {
		if files[i].Basename == "README.md" {
			readmeFile = &files[i]
			break
		}
	}

	if readmeFile == nil {
		t.Fatal("expected README.md file to be generated")
	}

	content := string(readmeFile.Content)
	if !strings.Contains(content, "Pet Store API") {
		t.Error("expected generated content to contain 'Pet Store API'")
	}
}

func TestGeneratedFile_Generate(t *testing.T) {
	tmpDir := t.TempDir()
	templatesDir := filepath.Join(tmpDir, "templates")
	outputDir := filepath.Join(tmpDir, "output")

	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		t.Fatalf("failed to create templates dir: %v", err)
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("failed to create output dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(templatesDir, "api.md.tmpl"), []byte(markdownTemplate), 0o644); err != nil {
		t.Fatalf("failed to write template: %v", err)
	}

	gen, err := NewGenerator(templatesDir, []byte(openAPI3Spec), outputDir)
	if err != nil {
		t.Fatalf("failed to create generator: %v", err)
	}

	files := gen.Files(context.Background())
	for _, file := range files {
		if err := file.Generate(outputDir); err != nil {
			t.Fatalf("failed to generate file %s: %v", file.Basename, err)
		}
	}

	entries, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatalf("failed to read output dir: %v", err)
	}

	if len(entries) == 0 {
		t.Error("expected output directory to contain generated files")
	}

	for _, file := range files {
		outputPath := filepath.Join(outputDir, file.Subdirectory, file.Basename)
		content, err := os.ReadFile(outputPath)
		if err != nil {
			t.Errorf("failed to read generated file %s: %v", outputPath, err)
			continue
		}

		if len(content) == 0 {
			t.Errorf("generated file %s is empty", outputPath)
		}
	}
}

func TestGenerator_NestedTemplates(t *testing.T) {
	tmpDir := t.TempDir()
	templatesDir := filepath.Join(tmpDir, "templates")
	outputDir := filepath.Join(tmpDir, "output")

	subDir := filepath.Join(templatesDir, "client", "go")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("failed to create nested dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(subDir, "client.go.tmpl"), []byte(goClientTemplate), 0o644); err != nil {
		t.Fatalf("failed to write template: %v", err)
	}

	gen, err := NewGenerator(templatesDir, []byte(openAPI3Spec), outputDir)
	if err != nil {
		t.Fatalf("failed to create generator: %v", err)
	}

	files := gen.Files(context.Background())

	if len(files) == 0 {
		t.Fatal("expected at least one generated file")
	}

	var clientFile *GeneratedFile
	for i := range files {
		if files[i].Basename == "client.go" {
			clientFile = &files[i]
			break
		}
	}

	if clientFile == nil {
		t.Fatal("expected client.go file to be generated")
	}

	if clientFile.Subdirectory != filepath.Join("client", "go") {
		t.Errorf("expected subdirectory 'client/go', got '%s'", clientFile.Subdirectory)
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("failed to create output dir: %v", err)
	}

	if err := clientFile.Generate(outputDir); err != nil {
		t.Fatalf("failed to generate file: %v", err)
	}

	expectedPath := filepath.Join(outputDir, "client", "go", "client.go")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Errorf("expected nested file at %s", expectedPath)
	}
}

func TestGenerator_InvalidSpec(t *testing.T) {
	tmpDir := t.TempDir()
	templatesDir := filepath.Join(tmpDir, "templates")

	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		t.Fatalf("failed to create templates dir: %v", err)
	}

	_, err := NewGenerator(templatesDir, []byte("invalid: yaml: content: ["), tmpDir)
	if err == nil {
		t.Error("expected error for invalid spec")
	}

	_, err = NewGenerator(templatesDir, []byte("foo: bar\nbaz: qux"), tmpDir)
	if err == nil {
		t.Error("expected error for non-OpenAPI spec")
	}
}

func TestTemplateFunctions_StringManipulation(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		fn       func(string) string
	}{
		{"camelCase simple", "hello_world", "helloWorld", toCamelCase},
		{"camelCase kebab", "hello-world", "helloWorld", toCamelCase},
		{"camelCase path", "/users/{id}/posts", "usersIdPosts", toCamelCase},
		{"pascalCase simple", "hello_world", "HelloWorld", toPascalCase},
		{"pascalCase kebab", "hello-world", "HelloWorld", toPascalCase},
		{"snakeCase simple", "HelloWorld", "hello_world", toSnakeCase},
		{"snakeCase camel", "helloWorld", "hello_world", toSnakeCase},
		{"kebabCase simple", "HelloWorld", "hello-world", toKebabCase},
		{"kebabCase snake", "hello_world", "hello-world", toKebabCase},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.fn(tt.input)
			if result != tt.expected {
				t.Errorf("expected '%s', got '%s'", tt.expected, result)
			}
		})
	}
}

func TestTemplateFunctions_TypeConversion(t *testing.T) {
	tests := []struct {
		name     string
		schema   *SchemaData
		expected string
		fn       func(*SchemaData) string
	}{
		{
			name:     "Go string",
			schema:   &SchemaData{Type: "string"},
			expected: "string",
			fn:       toGoType,
		},
		{
			name:     "Go integer",
			schema:   &SchemaData{Type: "integer", Format: "int64"},
			expected: "int64",
			fn:       toGoType,
		},
		{
			name:     "Go array",
			schema:   &SchemaData{Type: "array", Items: &SchemaData{Type: "string"}},
			expected: "[]string",
			fn:       toGoType,
		},
		{
			name:     "Go date-time",
			schema:   &SchemaData{Type: "string", Format: "date-time"},
			expected: "time.Time",
			fn:       toGoType,
		},
		{
			name:     "TypeScript string",
			schema:   &SchemaData{Type: "string"},
			expected: "string",
			fn:       toTypeScriptType,
		},
		{
			name:     "TypeScript number",
			schema:   &SchemaData{Type: "integer"},
			expected: "number",
			fn:       toTypeScriptType,
		},
		{
			name:     "TypeScript array",
			schema:   &SchemaData{Type: "array", Items: &SchemaData{Type: "string"}},
			expected: "string[]",
			fn:       toTypeScriptType,
		},
		{
			name:     "Python string",
			schema:   &SchemaData{Type: "string"},
			expected: "str",
			fn:       toPythonType,
		},
		{
			name:     "Python list",
			schema:   &SchemaData{Type: "array", Items: &SchemaData{Type: "integer"}},
			expected: "List[int]",
			fn:       toPythonType,
		},
		{
			name:     "Rust string",
			schema:   &SchemaData{Type: "string"},
			expected: "String",
			fn:       toRustType,
		},
		{
			name:     "Rust vec",
			schema:   &SchemaData{Type: "array", Items: &SchemaData{Type: "string"}},
			expected: "Vec<String>",
			fn:       toRustType,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.fn(tt.schema)
			if result != tt.expected {
				t.Errorf("expected '%s', got '%s'", tt.expected, result)
			}
		})
	}
}

func TestTemplateFunctions_PathHelpers(t *testing.T) {
	params := extractPathParams("/users/{userId}/posts/{postId}")
	if len(params) != 2 {
		t.Errorf("expected 2 params, got %d", len(params))
	}
	if params[0] != "userId" || params[1] != "postId" {
		t.Errorf("unexpected params: %v", params)
	}

	methodName := pathToMethodName("/users/{id}/posts")
	if methodName != "UsersIdPosts" {
		t.Errorf("expected 'UsersIdPosts', got '%s'", methodName)
	}

	cleaned := cleanPath("/users/posts/")
	if cleaned != "users/posts" {
		t.Errorf("expected 'users/posts', got '%s'", cleaned)
	}
}

func TestTemplateFunctions_Indent(t *testing.T) {
	input := "line1\nline2\nline3"
	result := indent(4, input)
	expected := "    line1\n    line2\n    line3"
	if result != expected {
		t.Errorf("expected:\n%s\n\ngot:\n%s", expected, result)
	}
}

func TestTemplateFunctions_EscapeString(t *testing.T) {
	input := "Hello \"World\"\nNew\tLine"
	result := escapeString(input)
	expected := "Hello \\\"World\\\"\\nNew\\tLine"
	if result != expected {
		t.Errorf("expected '%s', got '%s'", expected, result)
	}
}

func TestTemplateFunctions_IsPrimitive(t *testing.T) {
	tests := []struct {
		schema   *SchemaData
		expected bool
	}{
		{&SchemaData{Type: "string"}, true},
		{&SchemaData{Type: "integer"}, true},
		{&SchemaData{Type: "number"}, true},
		{&SchemaData{Type: "boolean"}, true},
		{&SchemaData{Type: "array"}, false},
		{&SchemaData{Type: "object"}, false},
		{nil, false},
	}

	for _, tt := range tests {
		typeName := "nil"
		if tt.schema != nil {
			typeName = tt.schema.Type
		}
		t.Run(typeName, func(t *testing.T) {
			result := isPrimitive(tt.schema)
			if result != tt.expected {
				t.Errorf("isPrimitive(%s): expected %v, got %v", typeName, tt.expected, result)
			}
		})
	}
}

func TestGenerator_TypeScriptTemplate(t *testing.T) {
	tmpDir := t.TempDir()
	templatesDir := filepath.Join(tmpDir, "templates")
	outputDir := filepath.Join(tmpDir, "output")

	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		t.Fatalf("failed to create templates dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(templatesDir, "types.ts.tmpl"), []byte(tsTypesTemplate), 0o644); err != nil {
		t.Fatalf("failed to write template: %v", err)
	}

	gen, err := NewGenerator(templatesDir, []byte(openAPI3Spec), outputDir)
	if err != nil {
		t.Fatalf("failed to create generator: %v", err)
	}

	files := gen.Files(context.Background())

	var tsFile *GeneratedFile
	for i := range files {
		if files[i].Basename == "types.ts" {
			tsFile = &files[i]
			break
		}
	}

	if tsFile == nil {
		t.Fatal("expected types.ts file to be generated")
	}

	content := string(tsFile.Content)

	if !strings.Contains(content, "export interface Pet") {
		t.Error("expected TypeScript Pet interface")
	}

	if !strings.Contains(content, "export interface CreatePetRequest") {
		t.Error("expected TypeScript CreatePetRequest interface")
	}
}

func TestGenerator_GoClientTemplate(t *testing.T) {
	tmpDir := t.TempDir()
	templatesDir := filepath.Join(tmpDir, "templates")
	outputDir := filepath.Join(tmpDir, "output")

	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		t.Fatalf("failed to create templates dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(templatesDir, "client.go.tmpl"), []byte(goClientTemplate), 0o644); err != nil {
		t.Fatalf("failed to write template: %v", err)
	}

	gen, err := NewGenerator(templatesDir, []byte(openAPI3Spec), outputDir)
	if err != nil {
		t.Fatalf("failed to create generator: %v", err)
	}

	files := gen.Files(context.Background())

	var goFile *GeneratedFile
	for i := range files {
		if files[i].Basename == "client.go" {
			goFile = &files[i]
			break
		}
	}

	if goFile == nil {
		t.Fatal("expected client.go file to be generated")
	}

	content := string(goFile.Content)

	if !strings.Contains(content, "package client") {
		t.Error("expected Go package declaration")
	}

	if !strings.Contains(content, "type Pet struct") {
		t.Error("expected Go Pet struct")
	}
}

func TestDerefBool(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name     string
		input    *bool
		expected bool
	}{
		{"nil returns false", nil, false},
		{"true pointer returns true", &trueVal, true},
		{"false pointer returns false", &falseVal, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := derefBool(tt.input)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestGenerator_Operations(t *testing.T) {
	tmpDir := t.TempDir()
	templatesDir := filepath.Join(tmpDir, "templates")
	outputDir := filepath.Join(tmpDir, "output")

	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		t.Fatalf("failed to create templates dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(templatesDir, "test.tmpl"), []byte("test"), 0o644); err != nil {
		t.Fatalf("failed to write template: %v", err)
	}

	gen, err := NewGenerator(templatesDir, []byte(openAPI3Spec), outputDir)
	if err != nil {
		t.Fatalf("failed to create generator: %v", err)
	}

	foundOperations := make(map[string]bool)
	for _, path := range gen.data.Paths {
		for _, op := range path.Operations {
			foundOperations[op.OperationID] = true
		}
	}

	expectedOps := []string{"listPets", "createPet", "getPet", "deletePet"}
	for _, opID := range expectedOps {
		if !foundOperations[opID] {
			t.Errorf("expected operation '%s' to be found", opID)
		}
	}
}

func TestGenerator_Parameters(t *testing.T) {
	tmpDir := t.TempDir()
	templatesDir := filepath.Join(tmpDir, "templates")
	outputDir := filepath.Join(tmpDir, "output")

	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		t.Fatalf("failed to create templates dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(templatesDir, "test.tmpl"), []byte("test"), 0o644); err != nil {
		t.Fatalf("failed to write template: %v", err)
	}

	gen, err := NewGenerator(templatesDir, []byte(openAPI3Spec), outputDir)
	if err != nil {
		t.Fatalf("failed to create generator: %v", err)
	}

	var listPetsOp *OperationData
	for _, path := range gen.data.Paths {
		for i := range path.Operations {
			if path.Operations[i].OperationID == "listPets" {
				listPetsOp = &path.Operations[i]
				break
			}
		}
	}

	if listPetsOp == nil {
		t.Fatal("expected listPets operation")
	}

	if len(listPetsOp.Parameters) != 2 {
		t.Errorf("expected 2 parameters for listPets, got %d", len(listPetsOp.Parameters))
	}

	var limitParam *ParameterData
	for i := range listPetsOp.Parameters {
		if listPetsOp.Parameters[i].Name == "limit" {
			limitParam = &listPetsOp.Parameters[i]
			break
		}
	}

	if limitParam == nil {
		t.Fatal("expected limit parameter")
	}

	if limitParam.In != "query" {
		t.Errorf("expected 'query', got '%s'", limitParam.In)
	}

	if limitParam.Schema == nil {
		t.Fatal("expected limit schema")
	}

	if limitParam.Schema.Type != "integer" {
		t.Errorf("expected 'integer', got '%s'", limitParam.Schema.Type)
	}
}

func TestGenerator_SecuritySchemes(t *testing.T) {
	tmpDir := t.TempDir()
	templatesDir := filepath.Join(tmpDir, "templates")
	outputDir := filepath.Join(tmpDir, "output")

	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		t.Fatalf("failed to create templates dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(templatesDir, "test.tmpl"), []byte("test"), 0o644); err != nil {
		t.Fatalf("failed to write template: %v", err)
	}

	gen, err := NewGenerator(templatesDir, []byte(openAPI3Spec), outputDir)
	if err != nil {
		t.Fatalf("failed to create generator: %v", err)
	}

	if len(gen.data.SecuritySchemes) != 2 {
		t.Errorf("expected 2 security schemes, got %d", len(gen.data.SecuritySchemes))
	}

	var bearerAuth *SecuritySchemeData
	for i := range gen.data.SecuritySchemes {
		if gen.data.SecuritySchemes[i].Name == "bearerAuth" {
			bearerAuth = &gen.data.SecuritySchemes[i]
			break
		}
	}

	if bearerAuth == nil {
		t.Fatal("expected bearerAuth security scheme")
	}

	if bearerAuth.Type != "http" {
		t.Errorf("expected type 'http', got '%s'", bearerAuth.Type)
	}

	if bearerAuth.Scheme != "bearer" {
		t.Errorf("expected scheme 'bearer', got '%s'", bearerAuth.Scheme)
	}

	if bearerAuth.BearerFormat != "JWT" {
		t.Errorf("expected bearer format 'JWT', got '%s'", bearerAuth.BearerFormat)
	}
}
