// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package openapi

import (
	"maps"
	"mime"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// isJSONMediaType reports whether mediaType is application/json or a
// structured +json type such as application/merge-patch+json.
func isJSONMediaType(mediaType string) bool {
	mt, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
		return false
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

// jsonContentType returns the JSON media type of content, preferring
// application/json over the other JSON media types, or "" when there is none.
func jsonContentType(content openapi3.Content) string {
	if content.Get("application/json") != nil {
		return "application/json"
	}
	for _, ct := range slices.Sorted(maps.Keys(content)) {
		if isJSONMediaType(ct) {
			return ct
		}
	}
	return ""
}

// jsonMedia returns the media of the JSON media type of content (see
// jsonContentType), or nil when there is none.
func jsonMedia(content openapi3.Content) *openapi3.MediaType {
	switch ct := jsonContentType(content); ct {
	case "":
		return nil
	case "application/json":
		return content.Get(ct)
	default:
		return content[ct]
	}
}

// responseJSONMedia returns the JSON media of the response with status code.
// Only 2xx and default responses take the +json types, since those are the
// bodies the templates read: an error body such as application/problem+json
// stays out, so it doesn't count as a second inline response.
func responseJSONMedia(code string, resp *openapi3.Response) *openapi3.MediaType {
	if strings.HasPrefix(code, "2") || code == "default" {
		return jsonMedia(resp.Content)
	}
	return resp.Content.Get("application/json")
}

// resolveParam returns the concrete Parameter for a ParameterRef, resolving a
// $ref against components/parameters when the value is not already populated.
func (tf *templateFuncs) resolveParam(ref *openapi3.ParameterRef) *openapi3.Parameter {
	if ref == nil {
		return nil
	}
	if ref.Value != nil {
		return ref.Value
	}
	if ref.Ref != "" {
		if tf.parser == nil || tf.parser.doc == nil || tf.parser.doc.Components == nil {
			return nil
		}
		name := extractTypeFromRef(ref.Ref)
		if p, ok := tf.parser.doc.Components.Parameters[name]; ok {
			return p.Value
		}
	}
	return nil
}

// paramsIn returns the resolved parameters of an operation located in the given
// `in` position ("path", "query", "header", "cookie"), preserving spec order.
// Parameters declared on the PathItem are inherited by every operation on the
// path unless an operation-level parameter with the same (name, in) overrides
// them; inherited parameters are listed before operation-specific ones.
func (tf *templateFuncs) paramsIn(po PathOperation, in string) []*openapi3.Parameter {
	if po.Operation == nil {
		return nil
	}

	// Collect the (name, in) keys defined at the operation level so inherited
	// path-item parameters they override are dropped.
	opKeys := make(map[string]bool)
	for _, ref := range po.Operation.Parameters {
		if p := tf.resolveParam(ref); p != nil {
			opKeys[p.In+"\x00"+p.Name] = true
		}
	}

	var out []*openapi3.Parameter
	// Inherited path-item parameters first (unless overridden by the operation).
	if po.PathItem != nil {
		for _, ref := range po.PathItem.Parameters {
			p := tf.resolveParam(ref)
			if p == nil || p.In != in {
				continue
			}
			if opKeys[p.In+"\x00"+p.Name] {
				continue
			}
			out = append(out, p)
		}
	}
	// Then operation-specific parameters.
	for _, ref := range po.Operation.Parameters {
		p := tf.resolveParam(ref)
		if p != nil && p.In == in {
			out = append(out, p)
		}
	}
	return out
}

// pathParameters returns an operation's path parameters (required, positional),
// including those inherited from the PathItem.
func (tf *templateFuncs) pathParameters(po PathOperation) []*openapi3.Parameter {
	return tf.paramsIn(po, "path")
}

// queryParameters returns an operation's query parameters, including those
// inherited from the PathItem.
func (tf *templateFuncs) queryParameters(po PathOperation) []*openapi3.Parameter {
	return tf.paramsIn(po, "query")
}

// requestJSONSchema returns the schema of an operation's JSON request body, or nil when there is none.
func (tf *templateFuncs) requestJSONSchema(op *openapi3.Operation) *openapi3.SchemaRef {
	if op == nil || op.RequestBody == nil || op.RequestBody.Value == nil {
		return nil
	}
	mt := jsonMedia(op.RequestBody.Value.Content)
	if mt == nil {
		return nil
	}
	return mt.Schema
}

// requestBodyRequired reports whether the operation's request body is required.
func requestBodyRequired(op *openapi3.Operation) bool {
	if op == nil || op.RequestBody == nil || op.RequestBody.Value == nil {
		return false
	}
	return op.RequestBody.Value.Required
}

// responseJSONSchema returns the schema of an operation's success response
// (preferring 2xx, falling back to `default`), or nil when there is no JSON
// body to decode.
func (tf *templateFuncs) responseJSONSchema(op *openapi3.Operation) *openapi3.SchemaRef {
	if op == nil || op.Responses == nil {
		return nil
	}
	// Prefer the lowest 2xx code, then `default`.
	var chosen *openapi3.ResponseRef
	for _, entry := range sortedResponseCodes(op.Responses) {
		if strings.HasPrefix(entry.Code, "2") {
			chosen = entry.Ref
			break
		}
	}
	if chosen == nil {
		chosen = op.Responses.Value("default")
	}
	if chosen == nil || chosen.Value == nil {
		return nil
	}
	mt := jsonMedia(chosen.Value.Content)
	if mt == nil {
		return nil
	}
	return mt.Schema
}

// tsTypeRef renders a *openapi3.SchemaRef as a TypeScript type: a $ref becomes
// the referenced type name, an inline schema is mapped structurally. Returns
// "void" for a nil ref so it can be used directly as a return type.
func (tf *templateFuncs) tsTypeRef(ref *openapi3.SchemaRef) string {
	if ref == nil {
		return "void"
	}
	if ref.Ref != "" {
		return refTsName(tf.parser, ref.Ref)
	}
	return schemaToTsTypeWithParser(ref.Value, tf.parser)
}
