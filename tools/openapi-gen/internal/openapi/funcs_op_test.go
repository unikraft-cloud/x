// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package openapi

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/assert"
)

func TestJSONContentType(t *testing.T) {
	cases := []struct {
		name    string
		types   []string
		want    string
		wantKey string
	}{
		{"none", nil, "", ""},
		{"json", []string{"application/json"}, "application/json", "application/json"},
		{"json with params", []string{"application/json; charset=utf-8"}, "application/json; charset=utf-8", "application/json; charset=utf-8"},
		{"merge patch", []string{"application/merge-patch+json"}, "application/merge-patch+json", "application/merge-patch+json"},
		{"json preferred", []string{"application/merge-patch+json", "application/json"}, "application/json", "application/json"},
		{"first +json", []string{"application/problem+json", "application/merge-patch+json"}, "application/merge-patch+json", "application/merge-patch+json"},
		{"not json", []string{"application/octet-stream", "text/event-stream"}, "", ""},
		{"json-ish", []string{"application/jsonl", "application/json+zip"}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := openapi3.Content{}
			for _, ct := range tc.types {
				content[ct] = &openapi3.MediaType{Example: ct}
			}
			assert.Equal(t, tc.want, jsonContentType(content))
			if tc.wantKey == "" {
				assert.Nil(t, jsonMedia(content))
			} else {
				assert.Same(t, content[tc.wantKey], jsonMedia(content))
			}
		})
	}
}

func TestResponseJSONMedia(t *testing.T) {
	response := func(ct string) *openapi3.Response {
		return &openapi3.Response{Content: openapi3.Content{ct: &openapi3.MediaType{Example: ct}}}
	}
	ok := response("application/vnd.thing+json")
	def := response("application/merge-patch+json")
	problem := response("application/problem+json")
	notFound := response("application/json")

	assert.Same(t, ok.Content["application/vnd.thing+json"], responseJSONMedia("200", ok))
	assert.Same(t, def.Content["application/merge-patch+json"], responseJSONMedia("default", def))
	assert.Nil(t, responseJSONMedia("404", problem))
	assert.Same(t, notFound.Content["application/json"], responseJSONMedia("410", notFound))
}
