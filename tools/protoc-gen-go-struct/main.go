// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"bytes"
	"embed"
	"flag"
	"maps"
	"path"
	"regexp"
	"strings"
	"text/template"

	"github.com/iancoleman/strcase"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	flagext "unikraft.com/x/tools/protoc-gen-go-struct/flag"
)

const pluginName = "unikraft.com/x/tools/protoc-gen-go-struct"

type TemplateData struct {
	PluginName      string
	BasePackage     string
	NativeTime      bool
	OmitPathParams  bool
	Version         string
	Package         string
	GoImportPath    string            // resolved Go import path for the current file
	Imports         map[string]string // package alias -> import path
	Structs         map[string]Struct
	Enums           []Enum
	PathParamFields map[string]map[string]bool // message name -> field names to omit
}

type Struct struct {
	Comment string
	Name    string
	Fields  []StructField
}

type StructField struct {
	Comment string
	Name    string
	Type    string
	Tags    string
}

type Enum struct {
	Name    string
	Comment string
	Values  []EnumValue
}

type EnumValue struct {
	ID      string
	Name    string
	Comment string
}

//go:embed struct.tmpl
var tmpl embed.FS

func main() {
	var flags flag.FlagSet
	basePackage := flags.String("base_package", "", "Base package to prefix imports")
	nativeTime := flags.Bool("native_time", false, "Use time.Time instead of timestamppb.Timestamp")
	omitPathParams := flags.Bool("omit_path_params", false, "Omit fields from input messages that are used as path parameters in HTTP methods")

	protogen.Options{
		ParamFunc: flags.Set,
	}.Run(func(plugin *protogen.Plugin) error {
		for _, file := range plugin.Files {
			if !file.Generate {
				continue
			}

			err := generateFile(plugin, file, *basePackage, *nativeTime, *omitPathParams)
			if err != nil {
				return err
			}
		}

		return nil
	})
}

func generateFile(plugin *protogen.Plugin, file *protogen.File, basePackage string, nativeTime bool, omitPathParams bool) error {
	templateData := &TemplateData{
		PluginName:      pluginName,
		BasePackage:     basePackage,
		NativeTime:      nativeTime,
		OmitPathParams:  omitPathParams,
		Package:         string(file.GoPackageName),
		GoImportPath:    resolveImportPath(string(file.Desc.Path()), basePackage),
		Imports:         make(map[string]string),
		PathParamFields: make(map[string]map[string]bool),
	}

	// If omitPathParams is enabled, extract path parameters from HTTP annotations
	if omitPathParams {
		templateData.PathParamFields = extractPathParamFields(file.Services)
	}

	templateData.Structs = templateData.getStructs(file.Messages...)
	templateData.Enums = templateData.getEnums(file.Enums)

	tmpl, err := template.ParseFS(tmpl, "struct.tmpl")
	if err != nil {
		return err
	}

	var content bytes.Buffer
	err = tmpl.Execute(&content, templateData)
	if err != nil {
		return err
	}

	generatedFile := plugin.NewGeneratedFile(file.GeneratedFilenamePrefix+"_struct.pb.go", file.GoImportPath)
	_, err = generatedFile.Write(content.Bytes())
	if err != nil {
		return err
	}

	return nil
}

// extractPathParamFields extracts path parameters from HTTP annotations on service methods
// and returns a map of message name -> set of field names that should be omitted.
func extractPathParamFields(services []*protogen.Service) map[string]map[string]bool {
	result := make(map[string]map[string]bool)

	for _, service := range services {
		for _, method := range service.Methods {
			rule, ok := proto.GetExtension(method.Desc.Options(), annotations.E_Http).(*annotations.HttpRule)
			if rule == nil || !ok {
				continue
			}

			var uri string
			if u := rule.GetGet(); u != "" {
				uri = u
			} else if u := rule.GetPost(); u != "" {
				uri = u
			} else if u := rule.GetPut(); u != "" {
				uri = u
			} else if u := rule.GetPatch(); u != "" {
				uri = u
			} else if u := rule.GetDelete(); u != "" {
				uri = u
			}

			if uri == "" {
				continue
			}

			// Extract path parameters from URI (e.g., {name} or :name)
			var pathParams []string
			for p := range strings.SplitSeq(uri, "/") {
				if len(p) > 0 && (p[0] == '{' && p[len(p)-1] == '}') {
					pathParams = append(pathParams, p[1:len(p)-1])
				} else if len(p) > 0 && p[0] == ':' {
					pathParams = append(pathParams, p[1:])
				}
			}

			if len(pathParams) == 0 {
				continue
			}

			// Map path params to message fields
			inputMsg := method.Input.GoIdent.GoName
			if result[inputMsg] == nil {
				result[inputMsg] = make(map[string]bool)
			}

			for _, param := range pathParams {
				// Convert path param to Go field name (CamelCase)
				fieldName := strcase.ToCamel(param)
				result[inputMsg][fieldName] = true
			}
		}
	}

	return result
}

// generatePackageAlias creates a package alias from a proto file path
// e.g. "common/v1/common.proto" -> "commonv1"
func generatePackageAlias(protoPath string) string {
	// Remove .proto extension and get directory path
	dir := path.Dir(protoPath)

	// Split the path and remove empty parts
	parts := strings.Split(dir, "/")
	var cleanParts []string
	for _, part := range parts {
		if part != "" && part != "." {
			cleanParts = append(cleanParts, part)
		}
	}

	// Join parts and remove non-alphanumeric characters
	alias := strings.Join(cleanParts, "")
	re := regexp.MustCompile(`[^a-zA-Z0-9]`)
	alias = re.ReplaceAllString(alias, "")

	return alias
}

// resolveImportPath generates the full import path given a proto file path and base package
func resolveImportPath(protoPath, basePackage string) string {
	// Remove .proto extension and get directory path
	dir := path.Dir(protoPath)

	if basePackage != "" {
		return basePackage + "/" + dir
	}
	return dir
}

func (td *TemplateData) getStructs(messages ...*protogen.Message) map[string]Struct {
	data := make(map[string]Struct)

	for _, m := range messages {
		s := Struct{
			Name:    strings.ReplaceAll(m.GoIdent.GoName, "_", ""),
			Comment: m.Comments.Leading.String(),
		}

		for _, field := range m.Fields {
			// Skip fields that are path parameters if omitPathParams is enabled
			if td.OmitPathParams {
				if fieldsToOmit, ok := td.PathParamFields[m.GoIdent.GoName]; ok {
					if fieldsToOmit[field.GoName] {
						continue
					}
				}
			}

			f := StructField{
				Name:    field.GoName,
				Comment: field.Comments.Leading.String(),
			}

			if field.Enum != nil {
				enumPath := string(field.Enum.Desc.ParentFile().Path())
				enumImportPath := resolveImportPath(enumPath, td.BasePackage)

				if enumImportPath != td.GoImportPath {
					alias := generatePackageAlias(enumPath)
					td.Imports[alias] = enumImportPath
					f.Type = alias + "." + string(field.Enum.Desc.Name())
				} else {
					f.Type = string(field.Enum.Desc.Name())
				}
			} else if field.Desc.Kind() == protoreflect.MessageKind {
				switch field.Desc.Message().FullName() {
				case "google.protobuf.Timestamp":
					f.Type = "timestamppb.Timestamp"
					if td.NativeTime {
						f.Type = "time.Time"
					}

					if field.Desc.HasOptionalKeyword() {
						f.Type = "*" + f.Type
					}
				case "google.protobuf.Empty":
					f.Type = "emptypb.Empty"
				case "google.protobuf.Value":
					f.Type = "*structpb.Value"
				default:
					// Check if the message is embedded (i.e., not top-level)
					if field.Message != nil && field.Desc.Message().Parent() != nil && field.Desc.Message().Parent().Parent() != nil {
						// Embedded message: prefix with parent type.
						f.Type = strings.ReplaceAll(m.GoIdent.GoName, "_", "") + string(field.Desc.Message().Name())
						field.Message.GoIdent.GoName = f.Type // Update GoIdent for embedded messages
					} else {
						messagePath := string(field.Desc.Message().ParentFile().Path())
						messageImportPath := resolveImportPath(messagePath, td.BasePackage)

						if messageImportPath != td.GoImportPath {
							alias := generatePackageAlias(messagePath)
							td.Imports[alias] = messageImportPath
							f.Type = alias + "." + string(field.Desc.Message().Name())
						} else {
							f.Type = string(field.Desc.Message().Name())
						}
					}
					if field.Desc.HasOptionalKeyword() {
						f.Type = "*" + f.Type
					}
					// Recursively handle nested messages.
					if parent := field.Desc.Message().Parent(); parent != nil {
						if _, ok := parent.(protoreflect.MessageDescriptor); ok {
							maps.Copy(data, td.getStructs(field.Message))
						}
					}
				}
			} else {
				f.Type = field.Desc.Kind().String()

				// Map protobuf types to Go types
				switch f.Type {
				case "float":
					f.Type = "float32"
				case "double":
					f.Type = "float64"
				case "bytes":
					f.Type = "[]byte"
				}

				// Add pointer for optional fields (except []byte which is already a reference type)
				if field.Desc.HasOptionalKeyword() && f.Type != "[]byte" {
					f.Type = "*" + f.Type
				}
			}

			var encodedName string

			if field.Desc.HasJSONName() {
				encodedName = field.Desc.JSONName()
			} else {
				encodedName = strcase.ToSnake(field.GoName)
			}
			if field.Desc.HasOptionalKeyword() || field.Desc.IsList() || field.Desc.IsMap() {
				encodedName += ",omitempty"
			}

			f.Tags = "json:\"" + encodedName + "\" yaml:\"" + encodedName + "\""

			// Check for flag_name extension
			if flagName, ok := flagext.GetFlagName(field.Desc.Options()); ok {
				f.Tags += " flag:\"" + flagName + "\""
			}

			// Check for flag_default extension
			if flagDefault, ok := flagext.GetFlagDefault(field.Desc.Options()); ok {
				f.Tags += " default:\"" + flagDefault + "\""
			}

			if field.Desc.IsList() {
				f.Type = "[]" + f.Type
			} else if field.Desc.IsMap() {
				entry := field.Message // protogen.Message representing the map entry
				keyField := entry.Fields[0]
				valueField := entry.Fields[1]

				keyKind := keyField.Desc.Kind().String()
				valueKind := valueField.Desc.Kind()

				// If the value is a message or enum, inspect further.
				var valueTypeName string
				switch valueKind {
				case protoreflect.MessageKind:
					valuePath := string(valueField.Desc.Message().ParentFile().Path())
					valueImportPath := resolveImportPath(valuePath, td.BasePackage)

					if valueImportPath != td.GoImportPath {
						alias := generatePackageAlias(valuePath)
						td.Imports[alias] = valueImportPath
						valueTypeName = alias + "." + string(valueField.Desc.Message().Name())
					} else {
						valueTypeName = string(valueField.Desc.Message().Name())
					}
				case protoreflect.EnumKind:
					valuePath := string(valueField.Desc.Enum().ParentFile().Path())
					valueImportPath := resolveImportPath(valuePath, td.BasePackage)

					if valueImportPath != td.GoImportPath {
						alias := generatePackageAlias(valuePath)
						td.Imports[alias] = valueImportPath
						valueTypeName = alias + "." + string(valueField.Desc.Enum().Name())
					} else {
						valueTypeName = string(valueField.Desc.Enum().Name())
					}
				default:
					valueTypeName = valueKind.String()
				}

				f.Type = "map[" + keyKind + "]" + valueTypeName
			}

			s.Fields = append(s.Fields, f)
		}

		data[strings.ReplaceAll(m.GoIdent.GoName, "_", "")] = s
	}

	return data
}

func camelToSnakeUpper(s string) string {
	var result strings.Builder
	for i, c := range s {
		if i > 0 && 'A' <= c && c <= 'Z' {
			result.WriteRune('_')
		}
		result.WriteRune(c)
	}
	return strings.ToUpper(result.String())
}

func (td *TemplateData) getEnums(enums []*protogen.Enum) []Enum {
	var data []Enum

	for _, e := range enums {
		s := Enum{
			Name:    e.GoIdent.GoName,
			Comment: e.Comments.Leading.String(),
		}

		for _, value := range e.Values {
			v := EnumValue{
				Name:    strcase.ToCamel(strings.TrimPrefix(value.GoIdent.GoName, s.Name+"_")),
				Comment: value.Comments.Leading.String(),
			}

			v.ID = strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(value.GoIdent.GoName, s.Name+"_"), camelToSnakeUpper(s.Name)+"_"))

			s.Values = append(s.Values, v)
		}

		data = append(data, s)
	}

	return data
}
