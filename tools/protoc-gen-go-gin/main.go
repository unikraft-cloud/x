// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"bytes"
	"flag"
	"strings"
	"text/template"

	_ "embed"

	"github.com/iancoleman/strcase"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const pluginName = "unikraft.com/x/tools/protoc-gen-go-gin"

type TemplateData struct {
	PluginName       string
	Package          string
	GoImportPath     protogen.GoImportPath
	Services         []Service
	StreamKeepalive  bool
	StreamDataPrefix string
	ExtraImports     map[string]string // import path -> alias
}

type Service struct {
	Name    string
	Comment string
	Methods []Method
}

type Method struct {
	Name              string
	Comment           string
	URI               string
	RequestMethod     string
	Input             *protogen.Message
	Output            *protogen.Message
	PathParams        []string
	IsStreamingServer bool
	BodyFieldName     string            // Name of the field that maps to the HTTP body (from google.api.http body option)
	BodyField         *protogen.Field   // The field that maps to the HTTP body
	BodyIsFullMessage bool              // True when body: "*" - entire message is the body
	QueryFields       []*protogen.Field // Fields that should be bound as query parameters
}

//go:embed gin.tmpl
var ginTmpl string

// goTypeForField returns the Go type string for a protogen field, qualified with package alias if needed.
func goTypeForField(f *protogen.Field, currentPkg protogen.GoImportPath, imports map[string]string, basePackage string) string {
	if f.Message != nil {
		return qualifiedGoType(f.Message.GoIdent, currentPkg, imports, basePackage)
	}
	if f.Enum != nil {
		return qualifiedGoType(f.Enum.GoIdent, currentPkg, imports, basePackage)
	}
	// Map scalar protobuf kinds to Go types
	switch f.Desc.Kind() {
	case protoreflect.BoolKind:
		return "bool"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return "int32"
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return "uint32"
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return "int64"
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return "uint64"
	case protoreflect.FloatKind:
		return "float32"
	case protoreflect.DoubleKind:
		return "float64"
	case protoreflect.StringKind:
		return "string"
	case protoreflect.BytesKind:
		return "[]byte"
	default:
		return "any"
	}
}

// qualifiedGoType returns the Go type qualified with package alias if it's from a different package.
func qualifiedGoType(ident protogen.GoIdent, currentPkg protogen.GoImportPath, imports map[string]string, basePackage string) string {
	if ident.GoImportPath == currentPkg {
		return ident.GoName
	}
	// Need to use qualified name
	importPath := string(ident.GoImportPath)
	if basePackage != "" && !strings.HasPrefix(importPath, basePackage) {
		importPath = strings.TrimSuffix(basePackage, "/") + "/" + strings.TrimPrefix(importPath, "/")
	}
	alias := derivePackageAlias(importPath)
	imports[importPath] = alias
	return alias + "." + ident.GoName
}

// derivePackageAlias derives a short package alias from an import path.
func derivePackageAlias(importPath string) string {
	parts := strings.Split(importPath, "/")
	if len(parts) == 0 {
		return "pkg"
	}
	lastPart := parts[len(parts)-1]
	// If the last part looks like a version (v1, v2, etc.), combine with previous part
	if len(parts) > 1 && len(lastPart) >= 2 && lastPart[0] == 'v' && lastPart[1] >= '0' && lastPart[1] <= '9' {
		return parts[len(parts)-2] + lastPart
	}
	return lastPart
}

func main() {
	var flags flag.FlagSet
	streamKeepalive := flags.Bool("stream_keepalive", true, "Send a regular keep-alive with a timestamp in streaming responses")
	streamDataPrefix := flags.String("stream_data_prefix", "data", "Set a prefix for data messages in streaming responses")
	basePackage := flags.String("base_package", "", "Base package to prepend to Go import paths")

	protogen.Options{
		ParamFunc: flags.Set,
	}.Run(func(plugin *protogen.Plugin) error {
		for _, file := range plugin.Files {
			if !file.Generate {
				continue
			}
			if err := generateFile(plugin, file, *streamKeepalive, *streamDataPrefix, *basePackage); err != nil {
				return err
			}
		}

		return nil
	})
}

func generateFile(plugin *protogen.Plugin, file *protogen.File, streamKeepalive bool, streamDataPrefix string, basePackage string) error {
	services := getHTTPServices(file.Services)

	// No service has http option.
	if len(services) == 0 {
		return nil
	}

	// Track extra imports needed for body/query field types from other packages
	extraImports := make(map[string]string)
	currentPkg := file.GoImportPath

	// goType is a closure that returns qualified types and tracks imports
	goType := func(f *protogen.Field) string {
		return goTypeForField(f, currentPkg, extraImports, basePackage)
	}

	// Pre-collect imports by iterating over all body and query fields.
	// This ensures imports are available before template rendering.
	for _, service := range services {
		for _, method := range service.Methods {
			if method.BodyField != nil {
				goType(method.BodyField)
			}
			for _, qf := range method.QueryFields {
				goType(qf)
			}
		}
	}

	templateData := TemplateData{
		PluginName:       pluginName,
		Package:          string(file.GoPackageName),
		GoImportPath:     file.GoImportPath,
		Services:         services,
		StreamKeepalive:  streamKeepalive,
		StreamDataPrefix: streamDataPrefix,
		ExtraImports:     extraImports,
	}

	tmpl, err := template.
		New("gin").
		Funcs(template.FuncMap{
			"toCamel":      strcase.ToCamel,
			"toLowerCamel": strcase.ToLowerCamel,
			"goType":       goType,
		}).
		Parse(ginTmpl)
	if err != nil {
		return err
	}

	var content bytes.Buffer
	err = tmpl.Execute(&content, templateData)
	if err != nil {
		return err
	}

	generatedFile := plugin.NewGeneratedFile(file.GeneratedFilenamePrefix+"_gin.pb.go", file.GoImportPath)
	_, err = generatedFile.Write(content.Bytes())
	if err != nil {
		return err
	}

	return nil
}

// getHTTPServices returns the http services data with their methods that has
// http options
func getHTTPServices(ps []*protogen.Service) []Service {
	var data []Service

	for _, service := range ps {
		sd := Service{
			Name:    service.GoName,
			Comment: service.Comments.Leading.String(),
		}

		for _, method := range service.Methods {
			// Skip client streaming for now
			if method.Desc.IsStreamingClient() {
				continue
			}

			rule, ok := proto.GetExtension(method.Desc.Options(), annotations.E_Http).(*annotations.HttpRule)
			if rule != nil && ok {
				m := Method{
					Name:              method.GoName,
					Comment:           method.Comments.Leading.String(),
					Input:             method.Input,
					Output:            method.Output,
					IsStreamingServer: method.Desc.IsStreamingServer(),
				}

				if u := rule.GetGet(); u != "" {
					m.RequestMethod = "GET"
					m.URI = u
				} else if u := rule.GetPost(); u != "" {
					m.RequestMethod = "POST"
					m.URI = u
				} else if u := rule.GetPut(); u != "" {
					m.RequestMethod = "PUT"
					m.URI = u
				} else if u := rule.GetPatch(); u != "" {
					m.RequestMethod = "PATCH"
					m.URI = u
				} else if u := rule.GetDelete(); u != "" {
					m.RequestMethod = "DELETE"
					m.URI = u
				}

				// Replace path parameters with colon prefixed names,
				// e.g. {name} -> :name
				paths := strings.Split(m.URI, "/")
				for i, p := range paths {
					if len(p) > 0 && (p[0] == '{' && p[len(p)-1] == '}' || p[0] == ':') {
						param := p[1 : len(p)-1]
						paths[i] = ":" + param
						m.PathParams = append(m.PathParams, param)
					}
				}
				m.URI = strings.Join(paths, "/")

				// Handle body field mapping from google.api.http body option
				bodyFieldName := rule.GetBody()
				if bodyFieldName == "*" {
					// body: "*" means the entire message is the body
					m.BodyIsFullMessage = true
				} else if bodyFieldName != "" {
					m.BodyFieldName = bodyFieldName
					// Find the body field in the input message
					for _, field := range method.Input.Fields {
						if field.Desc.JSONName() == bodyFieldName || string(field.Desc.Name()) == bodyFieldName {
							m.BodyField = field
							break
						}
					}
				}

				// Collect fields as query parameters (excluding the body field and path params)
				// Skip if body: "*" since the entire message is the body
				if !m.BodyIsFullMessage {
					for _, field := range method.Input.Fields {
						fieldName := field.Desc.JSONName()
						protoName := string(field.Desc.Name())
						// Skip if this is the body field
						if bodyFieldName != "" && (fieldName == bodyFieldName || protoName == bodyFieldName) {
							continue
						}
						// Skip if this is a path parameter
						isPathParam := false
						for _, pp := range m.PathParams {
							if fieldName == pp || protoName == pp {
								isPathParam = true
								break
							}
						}
						if !isPathParam {
							m.QueryFields = append(m.QueryFields, field)
						}
					}
				}

				sd.Methods = append(sd.Methods, m)
			}
		}

		if len(sd.Methods) > 0 {
			data = append(data, sd)
		}
	}

	return data
}
