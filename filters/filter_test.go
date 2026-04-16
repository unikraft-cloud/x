// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026, The containerd Authors.
// Licensed under the Apache License, Version 2.0 (the "License").
// You may not use this file except in compliance with the License.

package filters

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilters(t *testing.T) {
	type cEntry struct {
		Name         string
		Other        string
		Labels       map[string]string
		NestedLabels map[string]map[string]string
	}

	corpusS := []cEntry{
		{
			Name: "foo",
			Labels: map[string]string{
				"foo": "true",
			},
			NestedLabels: map[string]map[string]string{
				"x": {
					"foo": "true",
				},
				"y": {
					"bar": "true",
				},
			},
		},
		{
			Name: "bar",
			NestedLabels: map[string]map[string]string{
				"x": {
					"foo": "true",
				},
				"y": {
					"bar": "false",
				},
			},
		},
		{
			Name: "foo",
			Labels: map[string]string{
				"foo":                "present",
				"more complex label": "present",
			},
		},
		{
			Name: "bar",
			Labels: map[string]string{
				"bar": "true",
			},
		},
		{
			Name: "fooer",
			Labels: map[string]string{
				"more complex label with \\ and \"": "present",
			},
		},
		{
			Name: "fooer",
			Labels: map[string]string{
				"more complex label with \\ and \".post": "present",
			},
		},
		{
			Name:  "baz",
			Other: "too complex, yo",
		},
		{
			Name:  "bazo",
			Other: "abc",
		},
		{
			Name: "compound",
			Labels: map[string]string{
				"foo": "omg_asdf.asdf-qwer",
			},
		},
	}

	var corpus []any
	for _, entry := range corpusS {
		corpus = append(corpus, entry)
	}

	// adapt shows an example of how to build an adaptor function for a type.
	adapt := func(o any) Adaptor {
		obj := o.(cEntry)
		return AdapterFunc(func(fieldpath []string) (string, []string, bool) {
			switch fieldpath[0] {
			case "name":
				return obj.Name, nil, true
			case "other":
				return obj.Other, nil, true
			case "labels":
				if len(fieldpath) < 2 {
					return "", slices.Collect(maps.Keys(obj.Labels)), true
				}
				value, ok := obj.Labels[strings.Join(fieldpath[1:], ".")]
				if !ok {
					return "", nil, true
				}
				return value, nil, true
			case "nestedlabels":
				if len(fieldpath) < 2 {
					return "", slices.Collect(maps.Keys(obj.NestedLabels)), true
				}
				nested, ok := obj.NestedLabels[fieldpath[1]]
				if !ok {
					return "", nil, true
				}
				if len(fieldpath) < 3 {
					return "", slices.Collect(maps.Keys(nested)), true
				}
				value, ok := nested[strings.Join(fieldpath[2:], ".")]
				if !ok {
					return "", nil, true
				}
				return value, nil, true
			}

			return "", nil, false
		})
	}

	for _, testcase := range []struct {
		name      string
		input     string
		expected  []any
		errString string
		errField  string
	}{
		{
			name:     "Empty",
			input:    "",
			expected: corpus,
		},
		{
			name:     "Present",
			input:    "name",
			expected: corpus,
		},
		{
			name:  "LabelPresent",
			input: "labels.foo",
			expected: []any{
				corpus[0],
				corpus[2],
				corpus[8],
			},
		},
		{
			name:  "NameAndLabelPresent",
			input: "labels.foo,name",
			expected: []any{
				corpus[0],
				corpus[2],
				corpus[8],
			},
		},
		{
			name:  "LabelValue",
			input: "labels.foo==true",
			expected: []any{
				corpus[0],
			},
		},
		{
			name:  "LabelValuePunctuated",
			input: "labels.foo==omg_asdf.asdf-qwer",
			expected: []any{
				corpus[8],
			},
		},
		{
			name:      "LabelValueNoAltQuoting",
			input:     "labels.|foo|==omg_asdf.asdf-qwer",
			errString: "filters: parse error: [labels. >|||< foo|==omg_asdf.asdf-qwer]: invalid quote encountered",
		},
		{
			name:  "Name",
			input: "name==bar",
			expected: []any{
				corpus[1],
				corpus[3],
			},
		},
		{
			name:  "NameNotEqual",
			input: "name!=bar",
			expected: []any{
				corpus[0],
				corpus[2],
				corpus[4],
				corpus[5],
				corpus[6],
				corpus[7],
				corpus[8],
			},
		},
		{
			name:  "NameAndLabelPresent",
			input: "name==bar,labels.bar",
			expected: []any{
				corpus[3],
			},
		},
		{
			name:  "QuotedValue",
			input: "other==\"too complex, yo\"",
			expected: []any{
				corpus[6],
			},
		},
		{
			name:  "RegexpValue",
			input: "other~=[abc]+,name!=foo",
			expected: []any{
				corpus[6],
				corpus[7],
			},
		},
		{
			name:  "NotRegexpValue",
			input: "name==foo,labels.foo!~=p.*",
			expected: []any{
				corpus[0],
			},
		},
		{
			name:  "RegexpQuotedValue",
			input: "other~=/[abc]+/,name!=foo",
			expected: []any{
				corpus[6],
				corpus[7],
			},
		},
		{
			name:  "RegexpQuotedValue",
			input: "other~=/[abc]{1,2}/,name!=foo",
			expected: []any{
				corpus[6],
				corpus[7],
			},
		},
		{
			name:  "RegexpQuotedValueGarbage",
			input: "other~=/[abc]{0,1}\"\\//,name!=foo",
			// valid syntax, but doesn't match anything
		},
		{
			name:  "NameAndLabelValue",
			input: "name==bar,labels.bar==true",
			expected: []any{
				corpus[3],
			},
		},
		{
			name:  "NameAndLabelValueNoMatch",
			input: "name==bar,labels.bar==wrong",
		},
		{
			name:  "LabelQuotedFieldPathPresent",
			input: `name==foo,labels."more complex label"`,
			expected: []any{
				corpus[2],
			},
		},
		{
			name:  "LabelQuotedFieldPathPresentWithQuoted",
			input: `labels."more complex label with \\ and \""==present`,
			expected: []any{
				corpus[4],
			},
		},
		{
			name:  "LabelQuotedFieldPathPresentWithQuotedEmbed",
			input: `labels."more complex label with \\ and \"".post==present`,
			expected: []any{
				corpus[5],
			},
		},
		{
			name:      "LabelQuotedFieldPathPresentWithQuotedEmbedInvalid",
			input:     `labels.?"more complex label with \\ and \"".post==present`,
			errString: `filters: parse error: [labels. >|?|< "more complex label with \\ and \"".post==present]: expected field or quoted`,
		},
		{
			name:      "TrailingComma",
			input:     "name==foo,",
			errString: `filters: parse error: [name==foo,]: expected field or quoted`,
		},
		{
			name:      "TrailingFieldSeparator",
			input:     "labels.",
			errString: `filters: parse error: [labels.]: expected field or quoted`,
		},
		{
			name:      "MissingValue",
			input:     "image~=,id?=?fbaq",
			errString: `filters: parse error: [image~= >|,|< id?=?fbaq]: expected value or quoted`,
		},
		{
			name:      "FieldQuotedLiteralNotTerminated",
			input:     "labels.ns/key==value",
			errString: `filters: parse error: [labels.ns >|/|< key==value]: quoted literal not terminated`,
		},
		{
			name:      "ValueQuotedLiteralNotTerminated",
			input:     "labels.key==/value",
			errString: `filters: parse error: [labels.key== >|/|< value]: quoted literal not terminated`,
		},
		{
			name:  "WildcardValue",
			input: "labels.*==present",
			expected: []any{
				corpus[2],
				corpus[4],
				corpus[5],
			},
		},
		{
			name:  "WildcardPresent",
			input: "labels.*",
			expected: []any{
				corpus[0],
				corpus[2],
				corpus[3],
				corpus[4],
				corpus[5],
				corpus[8],
			},
		},
		{
			name:  "WildcardPresentAndName",
			input: "labels.*,name==foo",
			expected: []any{
				corpus[0],
				corpus[2],
			},
		},
		{
			name:  "WildcardNotEqual",
			input: "labels.*!=true",
			expected: []any{
				corpus[2],
				corpus[4],
				corpus[5],
				corpus[8],
			},
		},
		{
			name:  "WildcardMatches",
			input: "labels.*~=^true$",
			expected: []any{
				corpus[0],
				corpus[3],
			},
		},
		{
			name:  "WildcardNotMatches",
			input: "labels.*!~=^true$",
			expected: []any{
				corpus[2],
				corpus[4],
				corpus[5],
				corpus[8],
			},
		},
		{
			name:  "NestedWildcardValue",
			input: "nestedlabels.x.*==true",
			expected: []any{
				corpus[0],
				corpus[1],
			},
		},
		{
			name:  "NestedWildcardNotEqual",
			input: "nestedlabels.x.*!=true",
		},
		{
			name:  "NestedWildcardKey",
			input: "nestedlabels.*.foo==true",
			expected: []any{
				corpus[0],
				corpus[1],
			},
		},
		{
			name:  "NestedWildcardKeyNotEqual",
			input: "nestedlabels.*.bar!=true",
			expected: []any{
				corpus[1],
			},
		},
		{
			name:  "NestedDoubleWildcard",
			input: "nestedlabels.*.*==true",
			expected: []any{
				corpus[0],
				corpus[1],
			},
		},
		{
			name:  "NestedDoubleWildcardNotEqual",
			input: "nestedlabels.*.*!=true",
		},
		{
			name:     "MissingField",
			input:    "missing.field==value",
			errField: "missing.field",
		},
		{
			name:     "WildcardMissingField",
			input:    "labels.*.missing",
			errField: "labels.*.missing",
		},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			filter, err := Parse(testcase.input)
			if testcase.errString != "" {
				require.Error(t, err)
				require.Equal(t, testcase.errString, err.Error())

				return
			}
			require.NoError(t, err)

			require.NotNil(t, filter)

			var results []any
			sawErr := false
			for _, item := range corpus {
				adaptor := adapt(item)
				matched, err := filter.Match(adaptor)
				if err != nil {
					var fieldErr *FieldNotFoundError
					require.ErrorAs(t, err, &fieldErr)
					if testcase.errField != "" {
						require.Contains(t, err.Error(), testcase.errField)
					}
					sawErr = true
					continue
				}
				if matched {
					results = append(results, item)
				}
			}

			expectErr := testcase.errField != ""
			require.Equal(t, expectErr, sawErr, "field not found error expectation mismatch")

			require.Equal(t, testcase.expected, results, "%q: %#v != %#v", testcase.input, results, testcase.expected)
		})
	}
}

func TestOperatorStrings(t *testing.T) {
	for _, testcase := range []struct {
		op       operator
		expected string
	}{
		{operatorPresent, "?"},
		{operatorEqual, "="},
		{operatorNotEqual, "!="},
		{operatorMatches, "~="},
		{operatorNotMatches, "!~="},
		{10, "unknown"},
	} {
		require.Equal(t, testcase.expected, testcase.op.String())
	}
}

func FuzzFiltersParse(f *testing.F) {
	f.Add("foo=bar")
	f.Fuzz(func(t *testing.T, expr string) {
		filter, err := Parse(expr)
		require.False(t, filter != nil && err != nil, "either filter or err must be non-nil")
	})
}
