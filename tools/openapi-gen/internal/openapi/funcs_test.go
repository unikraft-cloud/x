// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package openapi

import (
	"testing"

	"github.com/ettle/strcase"
	"github.com/stretchr/testify/assert"
)

func TestIdentcase(t *testing.T) {
	cases := []struct {
		in        string
		pascal    string
		camel     string
		snake     string
		kebab     string
		screaming string
	}{
		{"name", "Name", "name", "name", "name", "NAME"},
		{"display_name", "DisplayName", "displayName", "display_name", "display-name", "DISPLAY_NAME"},
		{"pre-billing", "PreBilling", "preBilling", "pre_billing", "pre-billing", "PRE_BILLING"},
		{"invoice.paid", "InvoicePaid", "invoicePaid", "invoice_paid", "invoice-paid", "INVOICE_PAID"},
		{"NodeService", "NodeService", "nodeService", "node_service", "node-service", "NODE_SERVICE"},
		{"org:docs:enterprise", "OrgDocsEnterprise", "orgDocsEnterprise", "org_docs_enterprise", "org-docs-enterprise", "ORG_DOCS_ENTERPRISE"},
		{"image/png", "ImagePng", "imagePng", "image_png", "image-png", "IMAGE_PNG"},
		{"a+b", "AB", "aB", "a_b", "a-b", "A_B"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.pascal, identcase(strcase.ToPascal)(tc.in))
			assert.Equal(t, tc.camel, identcase(strcase.ToCamel)(tc.in))
			assert.Equal(t, tc.snake, identcase(strcase.ToSnake)(tc.in))
			assert.Equal(t, tc.kebab, identcase(strcase.ToKebab)(tc.in))
			assert.Equal(t, tc.screaming, identcase(strcase.ToSNAKE)(tc.in))
		})
	}
}

func TestIdentcaseKeepsValidNames(t *testing.T) {
	converts := map[string]func(string) string{
		"pascal": strcase.ToPascal,
		"camel":  strcase.ToCamel,
		"snake":  strcase.ToSnake,
		"kebab":  strcase.ToKebab,
		"SNAKE":  strcase.ToSNAKE,
	}
	names := []string{
		"", "a", "ID", "uuid", "ListNodes", "listNodes", "list_nodes",
		"list-nodes", "UnikraftCloud.Common.ResponseEnvelope", "v1beta2",
		"HTTPServer", "node service", "_private", "trailing_",
	}
	for convertName, convert := range converts {
		for _, name := range names {
			assert.Equal(t, convert(name), identcase(convert)(name), "%s(%q)", convertName, name)
		}
	}
}
