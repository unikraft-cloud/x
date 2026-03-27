// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"unicode"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel/high/base"
	v2high "github.com/pb33f/libopenapi/datamodel/high/v2"
	v3high "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/utils"
	"unikraft.com/x/log"
)

// TemplateData represents the data passed to templates for code generation.
type TemplateData struct {
	// Info contains the API metadata (title, version, description, etc.)
	Info *InfoData

	// Servers contains the list of server URLs (OpenAPI 3.x only)
	Servers []ServerData

	// Paths contains all the API paths and their operations
	Paths []PathData

	// Schemas contains all the schema definitions
	Schemas []SchemaData

	// Tags contains all the tags used in the API
	Tags []TagData

	// SecuritySchemes contains all security scheme definitions
	SecuritySchemes []SecuritySchemeData

	// Version is the OpenAPI/Swagger version string
	Version string

	// IsOpenAPI3 indicates if this is an OpenAPI 3.x spec
	IsOpenAPI3 bool

	// IsSwagger indicates if this is a Swagger 2.x spec
	IsSwagger bool
}

// InfoData represents the API info section.
type InfoData struct {
	Title          string
	Description    string
	Version        string
	TermsOfService string
	Contact        *ContactData
	License        *LicenseData
}

// ContactData represents contact information.
type ContactData struct {
	Name  string
	URL   string
	Email string
}

// LicenseData represents license information.
type LicenseData struct {
	Name string
	URL  string
}

// ServerData represents a server definition.
type ServerData struct {
	URL         string
	Description string
	Variables   map[string]ServerVariableData
}

// ServerVariableData represents a server variable.
type ServerVariableData struct {
	Default     string
	Description string
	Enum        []string
}

// PathData represents an API path with its operations.
type PathData struct {
	Path        string
	Summary     string
	Description string
	Operations  []OperationData
	Parameters  []ParameterData
}

// OperationData represents an HTTP operation on a path.
type OperationData struct {
	Method      string
	OperationID string
	Summary     string
	Description string
	Tags        []string
	Parameters  []ParameterData
	RequestBody *RequestBodyData
	Responses   []ResponseData
	Security    []SecurityRequirementData
	Deprecated  bool
}

// ParameterData represents an operation parameter.
type ParameterData struct {
	Name        string
	In          string
	Description string
	Required    bool
	Schema      *SchemaData
	Style       string
	Explode     bool
}

// RequestBodyData represents a request body.
type RequestBodyData struct {
	Description string
	Required    bool
	Content     map[string]*MediaTypeData
}

// MediaTypeData represents a media type definition.
type MediaTypeData struct {
	Schema  *SchemaData
	Example any
}

// ResponseData represents an operation response.
type ResponseData struct {
	StatusCode  string
	Description string
	Content     map[string]*MediaTypeData
	Headers     map[string]*HeaderData
}

// HeaderData represents a response header.
type HeaderData struct {
	Description string
	Schema      *SchemaData
	Required    bool
}

// SchemaData represents a schema definition.
type SchemaData struct {
	Name        string
	Type        string
	Format      string
	Description string
	Required    []string
	Properties  []PropertyData
	Items       *SchemaData
	Enum        []any
	Default     any
	Example     any
	Ref         string
	AllOf       []*SchemaData
	OneOf       []*SchemaData
	AnyOf       []*SchemaData
	Nullable    bool
	ReadOnly    bool
	WriteOnly   bool
	Minimum     *float64
	Maximum     *float64
	MinLength   *int64
	MaxLength   *int64
	Pattern     string
	MinItems    *int64
	MaxItems    *int64
	UniqueItems bool

	// AdditionalProperties for map types
	AdditionalProperties *SchemaData
}

// PropertyData represents a schema property.
type PropertyData struct {
	Name        string
	Schema      *SchemaData
	Required    bool
	Description string
}

// TagData represents an API tag.
type TagData struct {
	Name        string
	Description string
}

// SecuritySchemeData represents a security scheme.
type SecuritySchemeData struct {
	Name         string
	Type         string
	Description  string
	In           string
	Scheme       string
	BearerFormat string
	Flows        *OAuthFlowsData
}

// OAuthFlowsData represents OAuth2 flows.
type OAuthFlowsData struct {
	Implicit          *OAuthFlowData
	Password          *OAuthFlowData
	ClientCredentials *OAuthFlowData
	AuthorizationCode *OAuthFlowData
}

// OAuthFlowData represents a single OAuth2 flow.
type OAuthFlowData struct {
	AuthorizationURL string
	TokenURL         string
	RefreshURL       string
	Scopes           map[string]string
}

// SecurityRequirementData represents a security requirement.
type SecurityRequirementData struct {
	Name   string
	Scopes []string
}

// Generator handles the code generation from OpenAPI specs using templates.
type Generator struct {
	templatesDir   string
	outputDir      string
	data           *TemplateData
	templates      map[string]*template.Template
	nestedSchemas  map[string]SchemaData // Track nested schemas to be added
	definedSchemas map[string]bool       // Track which schemas are defined
}

// NewGenerator creates a new Generator instance.
func NewGenerator(templatesDir string, data []byte, outputDir string) (*Generator, error) {
	document, err := libopenapi.NewDocument(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse OpenAPI spec: %w", err)
	}

	g := &Generator{
		templatesDir:   templatesDir,
		outputDir:      outputDir,
		templates:      make(map[string]*template.Template),
		nestedSchemas:  make(map[string]SchemaData),
		definedSchemas: make(map[string]bool),
	}

	// Determine the spec type and build the appropriate model
	specInfo := document.GetSpecInfo()
	if specInfo.SpecType == utils.OpenApi3 {
		model, modelErr := document.BuildV3Model()
		if modelErr != nil {
			return nil, fmt.Errorf("failed to build OpenAPI 3 model: %w", modelErr)
		}
		g.data = g.extractOpenAPI3Data(&model.Model)
	} else if specInfo.SpecType == utils.OpenApi2 {
		model, modelErr := document.BuildV2Model()
		if modelErr != nil {
			return nil, fmt.Errorf("failed to build Swagger model: %w", modelErr)
		}
		g.data = g.extractSwaggerData(&model.Model)
	} else {
		return nil, fmt.Errorf("unsupported OpenAPI specification type")
	}

	// Load all templates
	if err := g.loadTemplates(); err != nil {
		return nil, fmt.Errorf("failed to load templates: %w", err)
	}

	return g, nil
}

// loadTemplates walks the templates directory and loads all .tmpl files.
func (g *Generator) loadTemplates() error {
	return filepath.Walk(g.templatesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".tmpl") {
			return nil
		}

		relPath, err := filepath.Rel(g.templatesDir, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path: %w", err)
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read template %s: %w", path, err)
		}

		tmpl, err := template.New(relPath).Funcs(g.templateFuncs()).Parse(string(content))
		if err != nil {
			return fmt.Errorf("failed to parse template %s: %w", path, err)
		}

		g.templates[relPath] = tmpl
		return nil
	})
}

// templateFuncs returns the custom functions available in templates.
func (g *Generator) templateFuncs() template.FuncMap {
	return template.FuncMap{
		// String manipulation
		"toLower":      strings.ToLower,
		"toUpper":      strings.ToUpper,
		"title":        strings.Title,
		"camelCase":    toCamelCase,
		"pascalCase":   toPascalCase,
		"snakeCase":    toSnakeCase,
		"kebabCase":    toKebabCase,
		"trimPrefix":   strings.TrimPrefix,
		"trimSuffix":   strings.TrimSuffix,
		"replace":      strings.ReplaceAll,
		"contains":     strings.Contains,
		"hasPrefix":    strings.HasPrefix,
		"hasSuffix":    strings.HasSuffix,
		"split":        strings.Split,
		"join":         strings.Join,
		"indent":       indent,
		"escapeString": escapeString,
		"quote":        func(s string) string { return fmt.Sprintf("%q", s) },
		// ptrType adds a pointer prefix to a type string unless it's already
		// a reference type (slice or map)
		"ptrType": func(typ string) string {
			if strings.HasPrefix(typ, "[]") || strings.HasPrefix(typ, "map[") {
				return typ
			}
			return "*" + typ
		},
		// isRefTypeStr checks if a type string is a reference type (slice or map)
		"isRefTypeStr": func(typ string) bool {
			return strings.HasPrefix(typ, "[]") || strings.HasPrefix(typ, "map[")
		},

		// Path manipulation
		"pathToMethodName": pathToMethodName,
		"pathParams":       extractPathParams,
		"cleanPath":        cleanPath,

		// Type helpers
		"goType":         toGoType,
		"goTypePtr":      toGoTypePtr,
		"jsonType":       toJSONType,
		"typeScriptType": toTypeScriptType,
		"pythonType":     toPythonType,
		"rustType":       toRustType,
		"isArray":        func(s *SchemaData) bool { return s != nil && s.Type == "array" },
		"isObject":       func(s *SchemaData) bool { return s != nil && s.Type == "object" },
		"isPrimitive":    isPrimitive,
		// isGoRefType returns true if the schema produces a Go reference type
		// (slice or map) that doesn't need a pointer
		"isGoRefType": func(s *SchemaData) bool {
			if s == nil {
				return false
			}
			typ := toGoType(s)
			return strings.HasPrefix(typ, "[]") || strings.HasPrefix(typ, "map[")
		},
		"isRequired": func(name string, required []string) bool {
			for _, r := range required {
				if r == name {
					return true
				}
			}
			return false
		},

		// HTTP helpers
		"isSuccessCode": func(code string) bool {
			// Treat "default" as success when it's the only response defined
			// Also treat 2xx codes as success
			return code == "default" || strings.HasPrefix(code, "2")
		},
		"httpMethodColor": func(method string) string {
			colors := map[string]string{
				"GET":    "green",
				"POST":   "blue",
				"PUT":    "orange",
				"DELETE": "red",
				"PATCH":  "purple",
			}
			if c, ok := colors[strings.ToUpper(method)]; ok {
				return c
			}
			return "gray"
		},

		// Collection helpers
		"first": func(items []string) string {
			if len(items) > 0 {
				return items[0]
			}
			return ""
		},
		"last": func(items []string) string {
			if len(items) > 0 {
				return items[len(items)-1]
			}
			return ""
		},
		"sortStrings": func(items []string) []string {
			sorted := make([]string, len(items))
			copy(sorted, items)
			sort.Strings(sorted)
			return sorted
		},

		// Conditional helpers
		"default": func(def, val any) any {
			if val == nil || val == "" {
				return def
			}
			return val
		},
		"ternary": func(cond bool, t, f any) any {
			if cond {
				return t
			}
			return f
		},

		// Comment helpers
		"comment": func(prefix, text string) string {
			if text == "" {
				return ""
			}
			lines := strings.Split(text, "\n")
			var result []string
			for _, line := range lines {
				result = append(result, prefix+" "+line)
			}
			return strings.Join(result, "\n")
		},
		"docComment": func(text string) string {
			if text == "" {
				return ""
			}
			lines := strings.Split(text, "\n")
			var result []string
			for _, line := range lines {
				result = append(result, "// "+line)
			}
			return strings.Join(result, "\n")
		},

		// Schema helpers
		"resolveRef": func(ref string) string {
			// Extract the schema name from a $ref like "#/components/schemas/Pet"
			// and normalize it to PascalCase to match generated type names
			parts := strings.Split(ref, "/")
			if len(parts) > 0 {
				return toPascalCase(parts[len(parts)-1])
			}
			return ref
		},
		"schemaName": func(s *SchemaData) string {
			if s == nil {
				return ""
			}
			if s.Ref != "" {
				parts := strings.Split(s.Ref, "/")
				return toPascalCase(parts[len(parts)-1])
			}
			return s.Name
		},
	}
}

// GeneratedFile represents a file to be generated.
type GeneratedFile struct {
	Basename     string
	TemplateName string
	Content      []byte
	Subdirectory string
}

// Files returns the list of files to be generated.
func (g *Generator) Files(ctx context.Context) []GeneratedFile {
	var files []GeneratedFile

	for tmplPath, tmpl := range g.templates {
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, g.data); err != nil {
			log.G(ctx).
				Warn().
				Err(err).
				Str("template", tmplPath).
				Msg("failed to execute template")
			// Skip templates that fail to execute (they might be partials)
			continue
		}

		// Determine output filename by removing .tmpl extension
		outputName := strings.TrimSuffix(tmplPath, ".tmpl")

		// Determine subdirectory from template path
		subdir := filepath.Dir(tmplPath)
		if subdir == "." {
			subdir = ""
		}

		files = append(files, GeneratedFile{
			Basename:     filepath.Base(outputName),
			TemplateName: tmplPath,
			Content:      buf.Bytes(),
			Subdirectory: subdir,
		})
	}

	return files
}

// Generate writes the generated file to the output directory.
func (f *GeneratedFile) Generate(outDir string) error {
	outputPath := filepath.Join(outDir, f.Subdirectory, f.Basename)

	// Create subdirectory if needed
	if f.Subdirectory != "" {
		if err := os.MkdirAll(filepath.Join(outDir, f.Subdirectory), 0o755); err != nil {
			return fmt.Errorf("failed to create subdirectory: %w", err)
		}
	}

	return os.WriteFile(outputPath, f.Content, 0o644)
}

// extractOpenAPI3Data extracts data from an OpenAPI 3.x document.
func (g *Generator) extractOpenAPI3Data(doc *v3high.Document) *TemplateData {
	data := &TemplateData{
		Version:    doc.Version,
		IsOpenAPI3: true,
		IsSwagger:  false,
	}

	// Extract info
	if doc.Info != nil {
		data.Info = &InfoData{
			Title:          doc.Info.Title,
			Description:    doc.Info.Description,
			Version:        doc.Info.Version,
			TermsOfService: doc.Info.TermsOfService,
		}
		if doc.Info.Contact != nil {
			data.Info.Contact = &ContactData{
				Name:  doc.Info.Contact.Name,
				URL:   doc.Info.Contact.URL,
				Email: doc.Info.Contact.Email,
			}
		}
		if doc.Info.License != nil {
			data.Info.License = &LicenseData{
				Name: doc.Info.License.Name,
				URL:  doc.Info.License.URL,
			}
		}
	}

	// Extract servers
	for _, server := range doc.Servers {
		s := ServerData{
			URL:         server.URL,
			Description: server.Description,
			Variables:   make(map[string]ServerVariableData),
		}
		if server.Variables != nil {
			for pair := server.Variables.Oldest(); pair != nil; pair = pair.Next() {
				v := pair.Value
				s.Variables[pair.Key] = ServerVariableData{
					Default:     v.Default,
					Description: v.Description,
					Enum:        v.Enum,
				}
			}
		}
		data.Servers = append(data.Servers, s)
	}

	// Extract tags
	for _, tag := range doc.Tags {
		data.Tags = append(data.Tags, TagData{
			Name:        tag.Name,
			Description: tag.Description,
		})
	}

	// Extract paths
	if doc.Paths != nil && doc.Paths.PathItems != nil {
		for pair := doc.Paths.PathItems.Oldest(); pair != nil; pair = pair.Next() {
			pathStr := pair.Key
			pathItem := pair.Value
			pathData := PathData{
				Path:        pathStr,
				Summary:     pathItem.Summary,
				Description: pathItem.Description,
			}

			// Extract path-level parameters
			for _, param := range pathItem.Parameters {
				pathData.Parameters = append(pathData.Parameters, g.extractV3Parameter(param))
			}

			// Extract operations
			operations := map[string]*v3high.Operation{
				"GET":     pathItem.Get,
				"POST":    pathItem.Post,
				"PUT":     pathItem.Put,
				"DELETE":  pathItem.Delete,
				"PATCH":   pathItem.Patch,
				"OPTIONS": pathItem.Options,
				"HEAD":    pathItem.Head,
				"TRACE":   pathItem.Trace,
			}

			for method, op := range operations {
				if op != nil {
					pathData.Operations = append(pathData.Operations, g.extractV3Operation(method, op))
				}
			}

			data.Paths = append(data.Paths, pathData)
		}
	}

	// Extract schemas from components
	if doc.Components != nil && doc.Components.Schemas != nil {
		for pair := doc.Components.Schemas.Oldest(); pair != nil; pair = pair.Next() {
			schemaProxy := pair.Value
			schema := schemaProxy.Schema()
			if schema != nil {
				normalizedName := toPascalCase(pair.Key)
				g.definedSchemas[normalizedName] = true
				data.Schemas = append(data.Schemas, g.extractV3Schema(pair.Key, schema))
			}
		}
	}

	// Add any nested schemas that were discovered during extraction
	for name, schema := range g.nestedSchemas {
		normalizedName := toPascalCase(name)
		if !g.definedSchemas[normalizedName] {
			data.Schemas = append(data.Schemas, schema)
			g.definedSchemas[normalizedName] = true
		}
	}

	// Extract security schemes
	if doc.Components != nil && doc.Components.SecuritySchemes != nil {
		for pair := doc.Components.SecuritySchemes.Oldest(); pair != nil; pair = pair.Next() {
			scheme := pair.Value
			data.SecuritySchemes = append(data.SecuritySchemes, g.extractV3SecurityScheme(pair.Key, scheme))
		}
	}

	// Collect all unique tags from operations and add any missing tags
	knownTags := make(map[string]bool)
	for _, tag := range data.Tags {
		knownTags[tag.Name] = true
	}
	for _, path := range data.Paths {
		for _, op := range path.Operations {
			for _, tagName := range op.Tags {
				if !knownTags[tagName] {
					knownTags[tagName] = true
					data.Tags = append(data.Tags, TagData{
						Name:        tagName,
						Description: "",
					})
				}
			}
		}
	}

	return data
}

// extractV3Parameter extracts parameter data from an OpenAPI 3.x parameter.
func (g *Generator) extractV3Parameter(param *v3high.Parameter) ParameterData {
	p := ParameterData{
		Name:        param.Name,
		In:          param.In,
		Description: param.Description,
		Required:    derefBool(param.Required),
		Style:       param.Style,
	}
	if param.Explode != nil {
		p.Explode = *param.Explode
	}
	if param.Schema != nil && param.Schema.Schema() != nil {
		schema := g.extractV3Schema("", param.Schema.Schema())
		p.Schema = &schema
	}
	return p
}

// extractV3Operation extracts operation data from an OpenAPI 3.x operation.
func (g *Generator) extractV3Operation(method string, op *v3high.Operation) OperationData {
	opData := OperationData{
		Method:      method,
		OperationID: op.OperationId,
		Summary:     op.Summary,
		Description: op.Description,
		Tags:        op.Tags,
		Deprecated:  derefBool(op.Deprecated),
	}

	// Extract parameters
	for _, param := range op.Parameters {
		opData.Parameters = append(opData.Parameters, g.extractV3Parameter(param))
	}

	// Extract request body
	if op.RequestBody != nil {
		rb := &RequestBodyData{
			Description: op.RequestBody.Description,
			Required:    derefBool(op.RequestBody.Required),
			Content:     make(map[string]*MediaTypeData),
		}
		if op.RequestBody.Content != nil {
			for pair := op.RequestBody.Content.Oldest(); pair != nil; pair = pair.Next() {
				mt := pair.Value
				mtData := &MediaTypeData{}
				if mt.Schema != nil {
					// Check if it's a reference first
					if mt.Schema.IsReference() {
						mtData.Schema = &SchemaData{
							Ref: mt.Schema.GetReference(),
						}
					} else if mt.Schema.Schema() != nil {
						schema := g.extractV3Schema("", mt.Schema.Schema())
						mtData.Schema = &schema
					}
				}
				if mt.Example != nil {
					mtData.Example = mt.Example.Value
				}
				rb.Content[pair.Key] = mtData
			}
		}
		opData.RequestBody = rb
	}

	// Extract responses
	if op.Responses != nil {
		// Helper function to extract a response
		extractResponse := func(statusCode string, resp *v3high.Response) ResponseData {
			respData := ResponseData{
				StatusCode:  statusCode,
				Description: resp.Description,
				Content:     make(map[string]*MediaTypeData),
				Headers:     make(map[string]*HeaderData),
			}
			if resp.Content != nil {
				for contentPair := resp.Content.Oldest(); contentPair != nil; contentPair = contentPair.Next() {
					mt := contentPair.Value
					mtData := &MediaTypeData{}
					if mt.Schema != nil {
						// Check if it's a reference first
						if mt.Schema.IsReference() {
							mtData.Schema = &SchemaData{
								Ref: mt.Schema.GetReference(),
							}
						} else if mt.Schema.Schema() != nil {
							schema := g.extractV3Schema("", mt.Schema.Schema())
							mtData.Schema = &schema
						}
					}
					respData.Content[contentPair.Key] = mtData
				}
			}
			if resp.Headers != nil {
				for headerPair := resp.Headers.Oldest(); headerPair != nil; headerPair = headerPair.Next() {
					header := headerPair.Value
					hData := &HeaderData{
						Description: header.Description,
						Required:    header.Required,
					}
					if header.Schema != nil {
						// Check if it's a reference first
						if header.Schema.IsReference() {
							hData.Schema = &SchemaData{
								Ref: header.Schema.GetReference(),
							}
						} else if header.Schema.Schema() != nil {
							schema := g.extractV3Schema("", header.Schema.Schema())
							hData.Schema = &schema
						}
					}
					respData.Headers[headerPair.Key] = hData
				}
			}
			return respData
		}

		// Extract responses from Codes map
		if op.Responses.Codes != nil {
			for pair := op.Responses.Codes.Oldest(); pair != nil; pair = pair.Next() {
				opData.Responses = append(opData.Responses, extractResponse(pair.Key, pair.Value))
			}
		}

		// Extract default response if present
		if op.Responses.Default != nil {
			opData.Responses = append(opData.Responses, extractResponse("default", op.Responses.Default))
		}
	}

	// Extract security requirements
	for _, secReq := range op.Security {
		if secReq.Requirements != nil {
			for secPair := secReq.Requirements.Oldest(); secPair != nil; secPair = secPair.Next() {
				opData.Security = append(opData.Security, SecurityRequirementData{
					Name:   secPair.Key,
					Scopes: secPair.Value,
				})
			}
		}
	}

	return opData
}

// extractV3Schema extracts schema data from an OpenAPI 3.x schema.
func (g *Generator) extractV3Schema(name string, schema *base.Schema) SchemaData {
	s := SchemaData{
		Name:        name,
		Description: schema.Description,
		Pattern:     schema.Pattern,
		UniqueItems: derefBool(schema.UniqueItems),
	}

	// Handle type (can be array in 3.1)
	if len(schema.Type) > 0 {
		s.Type = schema.Type[0]
	}
	s.Format = schema.Format
	s.Required = schema.Required

	// Handle nullable
	if schema.Nullable != nil {
		s.Nullable = *schema.Nullable
	}
	if schema.ReadOnly != nil {
		s.ReadOnly = *schema.ReadOnly
	}
	if schema.WriteOnly != nil {
		s.WriteOnly = *schema.WriteOnly
	}

	// Handle numeric constraints
	if schema.Minimum != nil {
		min := float64(*schema.Minimum)
		s.Minimum = &min
	}
	if schema.Maximum != nil {
		max := float64(*schema.Maximum)
		s.Maximum = &max
	}
	if schema.MinLength != nil {
		minLen := *schema.MinLength
		s.MinLength = &minLen
	}
	if schema.MaxLength != nil {
		maxLen := *schema.MaxLength
		s.MaxLength = &maxLen
	}
	if schema.MinItems != nil {
		minItems := *schema.MinItems
		s.MinItems = &minItems
	}
	if schema.MaxItems != nil {
		maxItems := *schema.MaxItems
		s.MaxItems = &maxItems
	}

	// Handle enum
	if schema.Enum != nil {
		for _, e := range schema.Enum {
			s.Enum = append(s.Enum, e.Value)
		}
	}

	// Handle default and example
	if schema.Default != nil {
		s.Default = schema.Default.Value
	}
	if schema.Example != nil {
		s.Example = schema.Example.Value
	}

	// Handle properties
	if schema.Properties != nil {
		for propPair := schema.Properties.Oldest(); propPair != nil; propPair = propPair.Next() {
			propSchema := propPair.Value.Schema()
			if propSchema != nil {
				prop := PropertyData{
					Name:        propPair.Key,
					Description: propSchema.Description,
				}
				extracted := g.extractV3Schema(propPair.Key, propSchema)

				// If this is an inline object with properties, register it as a nested schema
				if extracted.Type == "object" && len(extracted.Properties) > 0 && extracted.Name != "" {
					nestedName := toPascalCase(extracted.Name)
					if !g.definedSchemas[nestedName] {
						g.nestedSchemas[nestedName] = extracted
					}
				}

				prop.Schema = &extracted
				// Check if required
				for _, req := range schema.Required {
					if req == propPair.Key {
						prop.Required = true
						break
					}
				}
				s.Properties = append(s.Properties, prop)
			}
		}
	}

	// Handle items (for arrays)
	if schema.Items != nil && schema.Items.A != nil {
		if schema.Items.A.IsReference() {
			// Preserve the reference string for array items
			s.Items = &SchemaData{
				Ref: schema.Items.A.GetReference(),
			}
		} else if schema.Items.A.Schema() != nil {
			items := g.extractV3Schema("", schema.Items.A.Schema())
			s.Items = &items
		}
	}

	// Handle additional properties (for maps)
	if schema.AdditionalProperties != nil && schema.AdditionalProperties.A != nil {
		if schema.AdditionalProperties.A.IsReference() {
			// Preserve the reference string for additional properties
			s.AdditionalProperties = &SchemaData{
				Ref: schema.AdditionalProperties.A.GetReference(),
			}
		} else if schema.AdditionalProperties.A.Schema() != nil {
			addProps := g.extractV3Schema("", schema.AdditionalProperties.A.Schema())
			s.AdditionalProperties = &addProps
		}
	}

	// Helper function to check if a property exists and add it if not
	addPropertyIfNotExists := func(prop PropertyData, required bool) {
		for i, existing := range s.Properties {
			if existing.Name == prop.Name {
				// If the incoming property is required, mark the existing one as required too
				if required && !s.Properties[i].Required {
					s.Properties[i].Required = true
				}
				return
			}
		}
		prop.Required = required
		s.Properties = append(s.Properties, prop)
	}

	// Handle allOf, oneOf, anyOf
	for _, proxy := range schema.AllOf {
		if proxy.IsReference() {
			// Preserve the reference string for allOf items
			s.AllOf = append(s.AllOf, &SchemaData{
				Ref: proxy.GetReference(),
			})
		} else if proxy.Schema() != nil {
			sub := g.extractV3Schema("", proxy.Schema())
			s.AllOf = append(s.AllOf, &sub)
			// Merge properties from allOf sub-schemas (all properties are required if marked)
			for _, prop := range sub.Properties {
				addPropertyIfNotExists(prop, prop.Required)
			}
			// Also recursively merge from nested oneOf/anyOf
			for _, nested := range sub.OneOf {
				for _, prop := range nested.Properties {
					addPropertyIfNotExists(prop, false) // oneOf properties are optional
				}
			}
			for _, nested := range sub.AnyOf {
				for _, prop := range nested.Properties {
					addPropertyIfNotExists(prop, false) // anyOf properties are optional
				}
			}
		}
	}
	for _, proxy := range schema.OneOf {
		if proxy.IsReference() {
			s.OneOf = append(s.OneOf, &SchemaData{
				Ref: proxy.GetReference(),
			})
		} else if proxy.Schema() != nil {
			sub := g.extractV3Schema("", proxy.Schema())
			s.OneOf = append(s.OneOf, &sub)
			// Merge properties from oneOf sub-schemas (union of all possible properties)
			for _, prop := range sub.Properties {
				addPropertyIfNotExists(prop, false) // oneOf properties are optional
			}
		}
	}
	for _, proxy := range schema.AnyOf {
		if proxy.IsReference() {
			s.AnyOf = append(s.AnyOf, &SchemaData{
				Ref: proxy.GetReference(),
			})
		} else if proxy.Schema() != nil {
			sub := g.extractV3Schema("", proxy.Schema())
			s.AnyOf = append(s.AnyOf, &sub)
			// Merge properties from anyOf sub-schemas (union of all possible properties)
			for _, prop := range sub.Properties {
				addPropertyIfNotExists(prop, false) // anyOf properties are optional
			}
		}
	}

	return s
}

// extractV3SecurityScheme extracts security scheme data from OpenAPI 3.x.
func (g *Generator) extractV3SecurityScheme(name string, scheme *v3high.SecurityScheme) SecuritySchemeData {
	s := SecuritySchemeData{
		Name:         name,
		Type:         scheme.Type,
		Description:  scheme.Description,
		In:           scheme.In,
		Scheme:       scheme.Scheme,
		BearerFormat: scheme.BearerFormat,
	}

	if scheme.Flows != nil {
		s.Flows = &OAuthFlowsData{}
		if scheme.Flows.Implicit != nil {
			s.Flows.Implicit = g.extractOAuthFlow(scheme.Flows.Implicit)
		}
		if scheme.Flows.Password != nil {
			s.Flows.Password = g.extractOAuthFlow(scheme.Flows.Password)
		}
		if scheme.Flows.ClientCredentials != nil {
			s.Flows.ClientCredentials = g.extractOAuthFlow(scheme.Flows.ClientCredentials)
		}
		if scheme.Flows.AuthorizationCode != nil {
			s.Flows.AuthorizationCode = g.extractOAuthFlow(scheme.Flows.AuthorizationCode)
		}
	}

	return s
}

// extractOAuthFlow extracts OAuth flow data.
func (g *Generator) extractOAuthFlow(flow *v3high.OAuthFlow) *OAuthFlowData {
	f := &OAuthFlowData{
		AuthorizationURL: flow.AuthorizationUrl,
		TokenURL:         flow.TokenUrl,
		RefreshURL:       flow.RefreshUrl,
		Scopes:           make(map[string]string),
	}
	if flow.Scopes != nil {
		for pair := flow.Scopes.Oldest(); pair != nil; pair = pair.Next() {
			f.Scopes[pair.Key] = pair.Value
		}
	}
	return f
}

// extractSwaggerData extracts data from a Swagger 2.x document.
func (g *Generator) extractSwaggerData(doc *v2high.Swagger) *TemplateData {
	data := &TemplateData{
		Version:    doc.Swagger,
		IsOpenAPI3: false,
		IsSwagger:  true,
	}

	// Extract info
	if doc.Info != nil {
		data.Info = &InfoData{
			Title:          doc.Info.Title,
			Description:    doc.Info.Description,
			Version:        doc.Info.Version,
			TermsOfService: doc.Info.TermsOfService,
		}
		if doc.Info.Contact != nil {
			data.Info.Contact = &ContactData{
				Name:  doc.Info.Contact.Name,
				URL:   doc.Info.Contact.URL,
				Email: doc.Info.Contact.Email,
			}
		}
		if doc.Info.License != nil {
			data.Info.License = &LicenseData{
				Name: doc.Info.License.Name,
				URL:  doc.Info.License.URL,
			}
		}
	}

	// Extract servers from host/basePath/schemes
	if doc.Host != "" {
		scheme := "https"
		if len(doc.Schemes) > 0 {
			scheme = doc.Schemes[0]
		}
		data.Servers = append(data.Servers, ServerData{
			URL:         fmt.Sprintf("%s://%s%s", scheme, doc.Host, doc.BasePath),
			Description: "Default server",
		})
	}

	// Extract tags
	for _, tag := range doc.Tags {
		data.Tags = append(data.Tags, TagData{
			Name:        tag.Name,
			Description: tag.Description,
		})
	}

	// Extract paths
	if doc.Paths != nil && doc.Paths.PathItems != nil {
		for pair := doc.Paths.PathItems.Oldest(); pair != nil; pair = pair.Next() {
			pathStr := pair.Key
			pathItem := pair.Value
			pathData := PathData{
				Path: pathStr,
			}

			// Extract operations
			operations := map[string]*v2high.Operation{
				"GET":     pathItem.Get,
				"POST":    pathItem.Post,
				"PUT":     pathItem.Put,
				"DELETE":  pathItem.Delete,
				"PATCH":   pathItem.Patch,
				"OPTIONS": pathItem.Options,
				"HEAD":    pathItem.Head,
			}

			for method, op := range operations {
				if op != nil {
					pathData.Operations = append(pathData.Operations, g.extractSwaggerOperation(method, op))
				}
			}

			data.Paths = append(data.Paths, pathData)
		}
	}

	// Extract schemas from definitions
	if doc.Definitions != nil && doc.Definitions.Definitions != nil {
		for pair := doc.Definitions.Definitions.Oldest(); pair != nil; pair = pair.Next() {
			schemaProxy := pair.Value
			schema := schemaProxy.Schema()
			if schema != nil {
				normalizedName := toPascalCase(pair.Key)
				g.definedSchemas[normalizedName] = true
				data.Schemas = append(data.Schemas, g.extractV3Schema(pair.Key, schema))
			}
		}
	}

	// Add any nested schemas that were discovered during extraction
	for name, schema := range g.nestedSchemas {
		normalizedName := toPascalCase(name)
		if !g.definedSchemas[normalizedName] {
			data.Schemas = append(data.Schemas, schema)
			g.definedSchemas[normalizedName] = true
		}
	}

	// Extract security definitions
	if doc.SecurityDefinitions != nil && doc.SecurityDefinitions.Definitions != nil {
		for pair := doc.SecurityDefinitions.Definitions.Oldest(); pair != nil; pair = pair.Next() {
			scheme := pair.Value
			data.SecuritySchemes = append(data.SecuritySchemes, g.extractSwaggerSecurityScheme(pair.Key, scheme))
		}
	}

	// Collect all unique tags from operations and add any missing tags
	knownTags := make(map[string]bool)
	for _, tag := range data.Tags {
		knownTags[tag.Name] = true
	}
	for _, path := range data.Paths {
		for _, op := range path.Operations {
			for _, tagName := range op.Tags {
				if !knownTags[tagName] {
					knownTags[tagName] = true
					data.Tags = append(data.Tags, TagData{
						Name:        tagName,
						Description: "",
					})
				}
			}
		}
	}

	return data
}

// extractSwaggerOperation extracts operation data from a Swagger 2.x operation.
func (g *Generator) extractSwaggerOperation(method string, op *v2high.Operation) OperationData {
	opData := OperationData{
		Method:      method,
		OperationID: op.OperationId,
		Summary:     op.Summary,
		Description: op.Description,
		Tags:        op.Tags,
		Deprecated:  op.Deprecated,
	}

	// Extract parameters
	for _, param := range op.Parameters {
		p := ParameterData{
			Name:        param.Name,
			In:          param.In,
			Description: param.Description,
			Required:    derefBool(param.Required),
		}
		if param.Schema != nil && param.Schema.Schema() != nil {
			rawSchema := param.Schema.Schema()
			schema := g.extractV3Schema("", rawSchema)
			
			// For body parameters, check if we need to generate a named type
			if param.In == "body" {
				needsNamedType := false
				requestTypeName := ""
				
				// Case 1: allOf with refs + additional properties
				if len(rawSchema.AllOf) > 1 {
					hasRef := false
					hasInlineProps := false
					for _, allOfItem := range rawSchema.AllOf {
						if allOfItem.IsReference() {
							hasRef = true
						} else if allOfItem.Schema() != nil {
							s := allOfItem.Schema()
							if s.Properties != nil && s.Properties.Len() > 0 {
								hasInlineProps = true
							}
						}
					}
					if hasRef && hasInlineProps {
						needsNamedType = true
						requestTypeName = toPascalCase(op.OperationId) + "Request"
					}
				}
				
				// Case 2: inline object with title and properties
				if !needsNamedType && rawSchema.Title != "" && rawSchema.Properties != nil && rawSchema.Properties.Len() > 0 {
					needsNamedType = true
					requestTypeName = toPascalCase(rawSchema.Title)
				}
				
				// Case 3: inline object without title but with properties - generate name from operationId
				if !needsNamedType && len(rawSchema.Type) > 0 && rawSchema.Type[0] == "object" && rawSchema.Properties != nil && rawSchema.Properties.Len() > 0 {
					needsNamedType = true
					requestTypeName = toPascalCase(op.OperationId) + "Request"
				}
				
				// If we need a named type, set the name and register it
				if needsNamedType && requestTypeName != "" {
					schema.Name = requestTypeName
					if !g.definedSchemas[requestTypeName] {
						g.nestedSchemas[requestTypeName] = schema
					}
				}
			}
			
			p.Schema = &schema
		} else {
			// For non-body parameters in Swagger 2.0, the type info is on the parameter itself
			p.Schema = &SchemaData{
				Type:   param.Type,
				Format: param.Format,
			}
		}
		opData.Parameters = append(opData.Parameters, p)
	}

	// Extract responses
	if op.Responses != nil && op.Responses.Codes != nil {
		for pair := op.Responses.Codes.Oldest(); pair != nil; pair = pair.Next() {
			resp := pair.Value
			respData := ResponseData{
				StatusCode:  pair.Key,
				Description: resp.Description,
				Content:     make(map[string]*MediaTypeData),
			}
			if resp.Schema != nil {
				var schema SchemaData
				// Check if this is a reference
				if resp.Schema.IsReference() {
					schema = SchemaData{
						Ref: resp.Schema.GetReference(),
					}
				} else if resp.Schema.Schema() != nil {
					schema = g.extractV3Schema("", resp.Schema.Schema())
				}
				// In Swagger 2.0, we don't have media types per response
				respData.Content["application/json"] = &MediaTypeData{
					Schema: &schema,
				}
			}
			opData.Responses = append(opData.Responses, respData)
		}
	}

	// Extract security requirements
	for _, secReq := range op.Security {
		if secReq.Requirements != nil {
			for secPair := secReq.Requirements.Oldest(); secPair != nil; secPair = secPair.Next() {
				opData.Security = append(opData.Security, SecurityRequirementData{
					Name:   secPair.Key,
					Scopes: secPair.Value,
				})
			}
		}
	}

	return opData
}

// extractSwaggerSecurityScheme extracts security scheme data from Swagger 2.x.
func (g *Generator) extractSwaggerSecurityScheme(name string, scheme *v2high.SecurityScheme) SecuritySchemeData {
	s := SecuritySchemeData{
		Name:        name,
		Type:        scheme.Type,
		Description: scheme.Description,
		In:          scheme.In,
	}

	if scheme.Flow != "" {
		s.Flows = &OAuthFlowsData{}
		flow := &OAuthFlowData{
			AuthorizationURL: scheme.AuthorizationUrl,
			TokenURL:         scheme.TokenUrl,
			Scopes:           make(map[string]string),
		}
		if scheme.Scopes != nil && scheme.Scopes.Values != nil {
			for pair := scheme.Scopes.Values.Oldest(); pair != nil; pair = pair.Next() {
				flow.Scopes[pair.Key] = pair.Value
			}
		}
		switch scheme.Flow {
		case "implicit":
			s.Flows.Implicit = flow
		case "password":
			s.Flows.Password = flow
		case "application":
			s.Flows.ClientCredentials = flow
		case "accessCode":
			s.Flows.AuthorizationCode = flow
		}
	}

	return s
}

// derefBool safely dereferences a *bool pointer, returning false if nil.
func derefBool(b *bool) bool {
	if b == nil {
		return false
	}
	return *b
}

// Helper functions for templates

func toCamelCase(s string) string {
	words := splitWords(s)
	for i := 1; i < len(words); i++ {
		words[i] = strings.Title(strings.ToLower(words[i]))
	}
	if len(words) > 0 {
		words[0] = strings.ToLower(words[0])
	}
	return strings.Join(words, "")
}

func toPascalCase(s string) string {
	words := splitWords(s)
	for i := range words {
		words[i] = strings.Title(strings.ToLower(words[i]))
	}
	return strings.Join(words, "")
}

func toSnakeCase(s string) string {
	words := splitWords(s)
	for i := range words {
		words[i] = strings.ToLower(words[i])
	}
	return strings.Join(words, "_")
}

func toKebabCase(s string) string {
	words := splitWords(s)
	for i := range words {
		words[i] = strings.ToLower(words[i])
	}
	return strings.Join(words, "-")
}

func splitWords(s string) []string {
	// Handle camelCase, PascalCase, snake_case, kebab-case, dots, and path segments
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, "/", " ")
	s = strings.ReplaceAll(s, "{", " ")
	s = strings.ReplaceAll(s, "}", " ")
	s = strings.ReplaceAll(s, ".", " ")

	// Insert space before uppercase letters (for camelCase/PascalCase)
	var result strings.Builder
	for i, r := range s {
		if i > 0 && unicode.IsUpper(r) {
			prev := rune(s[i-1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) {
				result.WriteRune(' ')
			}
		}
		result.WriteRune(r)
	}

	words := strings.Fields(result.String())
	return words
}

func indent(spaces int, s string) string {
	pad := strings.Repeat(" ", spaces)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = pad + line
		}
	}
	return strings.Join(lines, "\n")
}

func escapeString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\t", "\\t")
	return s
}

func pathToMethodName(path string) string {
	// Convert /users/{id}/posts to UsersIdPosts
	return toPascalCase(path)
}

func extractPathParams(path string) []string {
	re := regexp.MustCompile(`\{([^}]+)\}`)
	matches := re.FindAllStringSubmatch(path, -1)
	var params []string
	for _, match := range matches {
		if len(match) > 1 {
			params = append(params, match[1])
		}
	}
	return params
}

func cleanPath(path string) string {
	// Remove leading/trailing slashes and clean up
	return strings.Trim(path, "/")
}

func toGoType(schema *SchemaData) string {
	if schema == nil {
		return "any"
	}
	if schema.Ref != "" {
		parts := strings.Split(schema.Ref, "/")
		return toPascalCase(parts[len(parts)-1])
	}
	// Handle named schemas with properties (inline objects that have been given a name)
	// This should come before allOf/type checks to properly handle generated request types
	if schema.Name != "" && len(schema.Properties) > 0 {
		return toPascalCase(schema.Name)
	}
	// Handle allOf schemas
	if len(schema.AllOf) > 0 {
		// Otherwise use the first referenced type as the base
		for _, sub := range schema.AllOf {
			if sub.Ref != "" {
				parts := strings.Split(sub.Ref, "/")
				return toPascalCase(parts[len(parts)-1])
			}
		}
		// If no ref found but there are allOf entries, try the first one
		if schema.AllOf[0] != nil {
			return toGoType(schema.AllOf[0])
		}
	}
	switch schema.Type {
	case "string":
		switch schema.Format {
		case "date-time":
			return "time.Time"
		case "date":
			return "string"
		case "binary":
			return "[]byte"
		case "uuid":
			return "string"
		default:
			return "string"
		}
	case "integer":
		switch schema.Format {
		case "int32":
			return "int32"
		case "int64":
			return "int64"
		default:
			return "int"
		}
	case "number":
		switch schema.Format {
		case "float":
			return "float32"
		case "double":
			return "float64"
		default:
			return "float64"
		}
	case "boolean":
		return "bool"
	case "array":
		if schema.Items != nil {
			return "[]" + toGoType(schema.Items)
		}
		return "[]any"
	case "object":
		if schema.AdditionalProperties != nil {
			return "map[string]" + toGoType(schema.AdditionalProperties)
		}
		// For inline objects with properties and a name, use the named type
		if len(schema.Properties) > 0 && schema.Name != "" {
			return toPascalCase(schema.Name)
		}
		// For objects with no properties (empty objects), use map[string]any
		if len(schema.Properties) == 0 {
			return "map[string]any"
		}
		return "map[string]any"
	default:
		return "any"
	}
}

// toGoTypePtr returns the Go type with a pointer prefix, but avoids redundant
// pointers for types that are already reference types (slices and maps).
func toGoTypePtr(schema *SchemaData) string {
	typ := toGoType(schema)
	// Don't add pointer for slices or maps - they're already reference types
	if strings.HasPrefix(typ, "[]") || strings.HasPrefix(typ, "map[") {
		return typ
	}
	return "*" + typ
}

func toJSONType(schema *SchemaData) string {
	if schema == nil {
		return "any"
	}
	switch schema.Type {
	case "string":
		return "string"
	case "integer", "number":
		return "number"
	case "boolean":
		return "boolean"
	case "array":
		return "array"
	case "object":
		return "object"
	default:
		return "any"
	}
}

func toTypeScriptType(schema *SchemaData) string {
	if schema == nil {
		return "any"
	}
	if schema.Ref != "" {
		parts := strings.Split(schema.Ref, "/")
		return parts[len(parts)-1]
	}
	switch schema.Type {
	case "string":
		return "string"
	case "integer", "number":
		return "number"
	case "boolean":
		return "boolean"
	case "array":
		if schema.Items != nil {
			return toTypeScriptType(schema.Items) + "[]"
		}
		return "any[]"
	case "object":
		if schema.AdditionalProperties != nil {
			return "Record<string, " + toTypeScriptType(schema.AdditionalProperties) + ">"
		}
		if schema.Name != "" {
			return schema.Name
		}
		return "Record<string, any>"
	default:
		return "any"
	}
}

func toPythonType(schema *SchemaData) string {
	if schema == nil {
		return "Any"
	}
	if schema.Ref != "" {
		parts := strings.Split(schema.Ref, "/")
		return parts[len(parts)-1]
	}
	switch schema.Type {
	case "string":
		switch schema.Format {
		case "date-time", "date":
			return "datetime"
		default:
			return "str"
		}
	case "integer":
		return "int"
	case "number":
		return "float"
	case "boolean":
		return "bool"
	case "array":
		if schema.Items != nil {
			return "List[" + toPythonType(schema.Items) + "]"
		}
		return "List[Any]"
	case "object":
		if schema.AdditionalProperties != nil {
			return "Dict[str, " + toPythonType(schema.AdditionalProperties) + "]"
		}
		if schema.Name != "" {
			return schema.Name
		}
		return "Dict[str, Any]"
	default:
		return "Any"
	}
}

func toRustType(schema *SchemaData) string {
	if schema == nil {
		return "serde_json::Value"
	}
	if schema.Ref != "" {
		parts := strings.Split(schema.Ref, "/")
		return parts[len(parts)-1]
	}
	switch schema.Type {
	case "string":
		return "String"
	case "integer":
		switch schema.Format {
		case "int32":
			return "i32"
		case "int64":
			return "i64"
		default:
			return "i64"
		}
	case "number":
		switch schema.Format {
		case "float":
			return "f32"
		default:
			return "f64"
		}
	case "boolean":
		return "bool"
	case "array":
		if schema.Items != nil {
			return "Vec<" + toRustType(schema.Items) + ">"
		}
		return "Vec<serde_json::Value>"
	case "object":
		if schema.AdditionalProperties != nil {
			return "std::collections::HashMap<String, " + toRustType(schema.AdditionalProperties) + ">"
		}
		if schema.Name != "" {
			return schema.Name
		}
		return "std::collections::HashMap<String, serde_json::Value>"
	default:
		return "serde_json::Value"
	}
}

func isPrimitive(schema *SchemaData) bool {
	if schema == nil {
		return false
	}
	switch schema.Type {
	case "string", "integer", "number", "boolean":
		return true
	default:
		return false
	}
}
