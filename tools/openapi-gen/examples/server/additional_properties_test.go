// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package server_test

import (
	"encoding/json"
	"encoding/json/jsontext"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/tools/openapi-gen/examples/server"
)

func TestAdditionalPropertiesMarshal(t *testing.T) {
	req := server.CreateWidgetRequest{
		Name: "widget",
		AdditionalProperties: map[string]jsontext.Value{
			"colour": jsontext.Value(`"red"`),
		},
	}

	out, err := json.Marshal(req)
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out, &raw))

	assert.NotContains(t, raw, "AdditionalProperties", `the catch-all leaked as a literal member; the json:",embed" tag option is not being honoured`)
	assert.Contains(t, raw, "colour")
}

func TestAdditionalPropertiesUnmarshal(t *testing.T) {
	var req server.CreateWidgetRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":"widget","colour":"red"}`), &req))

	assert.Equal(t, "widget", req.Name)
	assert.Equal(t, jsontext.Value(`"red"`), req.AdditionalProperties["colour"])
}
