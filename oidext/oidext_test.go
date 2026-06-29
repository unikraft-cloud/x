// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package oidext

import (
	"crypto/x509/pkix"
	"encoding/asn1"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type Inner struct {
	Role string `oid:"10"`
}

type Embedded struct {
	Region string `oid:"9"`
}

type HostAttrs struct {
	Hostname    string    `oid:"1,critical"`
	Fingerprint []byte    `oid:"2"`
	BootCount   int       `oid:"3"`
	Enabled     bool      `oid:"4"`
	Tags        []string  `oid:"5"`
	When        time.Time `oid:"6"`
	Inner       Inner     `oid:"7"`
	Optional    *string   `oid:"8,omitempty"`
	Embedded    `oid:",inline"`
	SkipMe      string `oid:"-"` // explicit skip
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	prefix := asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 0} // arbitrary example

	opt := "hello"
	in := HostAttrs{
		Hostname:    "node-01",
		Fingerprint: []byte{0xde, 0xad, 0xbe, 0xef},
		BootCount:   42,
		Enabled:     true,
		Tags:        []string{"a", "b", "c"},
		When:        time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC),
		Inner:       Inner{Role: "worker"},
		Optional:    &opt,
		Embedded:    Embedded{Region: "eu-west"},
		SkipMe:      "should not appear",
	}

	exts, err := Encode(prefix, in)
	require.NoError(t, err)

	// Ensure Hostname extension is present and critical.
	wantHostnameOID := append(prefix, 1)
	var hostnameExt *pkix.Extension
	for i := range exts {
		if exts[i].Id.String() == wantHostnameOID.String() {
			hostnameExt = &exts[i]
			break
		}
	}
	require.NotNil(t, hostnameExt, "hostname extension not found")
	assert.True(t, hostnameExt.Critical, "hostname extension should be critical")

	var out HostAttrs
	err = Decode(prefix, exts, &out, WithDecodeIgnoreUnknown())
	require.NoError(t, err)

	// SkipMe is tagged via "-", should remain zero.
	assert.Empty(t, out.SkipMe)

	assert.Equal(t, in.Hostname, out.Hostname)
	assert.Equal(t, in.Fingerprint, out.Fingerprint)
	assert.Equal(t, in.BootCount, out.BootCount)
	assert.Equal(t, in.Enabled, out.Enabled)
	assert.Equal(t, in.Tags, out.Tags)
	assert.True(t, in.When.Equal(out.When))
	assert.Equal(t, in.Inner, out.Inner)
	assert.Equal(t, in.Embedded, out.Embedded)
	require.NotNil(t, out.Optional)
	assert.Equal(t, *in.Optional, *out.Optional)
}

func TestOmitemptyPointer(t *testing.T) {
	prefix := asn1.ObjectIdentifier{1, 2, 3}

	in := HostAttrs{
		Hostname:    "x",
		Fingerprint: []byte{1, 2, 3},
		BootCount:   1,
		Enabled:     false,
		Tags:        nil,
		When:        time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC),
		Inner:       Inner{Role: "r"},
		Optional:    nil, // omitempty
	}

	exts, err := Encode(prefix, in)
	require.NoError(t, err)

	// Optional (suffix 8) should not be present.
	optionalOID := append(prefix, 8)
	for _, e := range exts {
		assert.NotEqual(t, optionalOID.String(), e.Id.String(), "optional extension should not be present")
	}

	var out HostAttrs
	err = Decode(prefix, exts, &out)
	require.NoError(t, err)
	assert.Nil(t, out.Optional)
}

func TestRequireAll(t *testing.T) {
	prefix := asn1.ObjectIdentifier{1, 2, 3}

	// Only include one extension.
	only := []pkix.Extension{
		{Id: append(prefix, 1), Critical: true, Value: mustDER(t, "node")},
	}

	var out HostAttrs
	err := Decode(prefix, only, &out, WithDecodeRequireAll())
	require.Error(t, err)
}

func TestIgnoreUntagged(t *testing.T) {
	type WithExtra struct {
		A string `oid:"1"`
		B string // untagged
	}

	prefix := asn1.ObjectIdentifier{9, 9, 9}
	in := WithExtra{A: "x", B: "y"}

	_, err := Encode(prefix, in)
	require.Error(t, err, "expected error for missing oid tag when IgnoreUntagged=false")

	exts, err := Encode(prefix, in, WithEncodeIgnoreUntagged())
	require.NoError(t, err)
	assert.Len(t, exts, 1)
}

func TestInspect(t *testing.T) {
	prefix := asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 0}

	var h HostAttrs

	tests := []struct {
		name      string
		structPtr any
		fieldPtr  any
		wantOID   asn1.ObjectIdentifier
		wantErr   bool
	}{
		{
			name:      "top-level field",
			structPtr: &h,
			fieldPtr:  &h.Hostname,
			wantOID:   append(append(asn1.ObjectIdentifier{}, prefix...), 1),
		},
		{
			name:      "another top-level field",
			structPtr: &h,
			fieldPtr:  &h.BootCount,
			wantOID:   append(append(asn1.ObjectIdentifier{}, prefix...), 3),
		},
		{
			name:      "inline field",
			structPtr: &h,
			fieldPtr:  &h.Region,
			wantOID:   append(append(asn1.ObjectIdentifier{}, prefix...), 9),
		},
		{
			name:      "nested struct field",
			structPtr: &h.Inner,
			fieldPtr:  &h.Inner.Role,
			wantOID:   append(append(asn1.ObjectIdentifier{}, prefix...), 10),
		},
		{
			name:      "nil structPtr",
			structPtr: (*HostAttrs)(nil),
			fieldPtr:  &h.Hostname,
			wantErr:   true,
		},
		{
			name:      "non-pointer structPtr",
			structPtr: h,
			fieldPtr:  &h.Hostname,
			wantErr:   true,
		},
		{
			name:      "nil fieldPtr",
			structPtr: &h,
			fieldPtr:  (*string)(nil),
			wantErr:   true,
		},
		{
			name:      "unrelated pointer",
			structPtr: &h,
			fieldPtr:  new(string),
			wantErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Inspect(prefix, tc.structPtr, tc.fieldPtr)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantOID.String(), got.String())
		})
	}
}

func mustDER(t *testing.T, s string) []byte {
	t.Helper()
	der, err := asn1.Marshal(s)
	require.NoError(t, err)
	return der
}

func TestFloatEncoding(t *testing.T) {
	type Floats struct {
		F32 float32 `oid:"1"`
		F64 float64 `oid:"2"`
	}

	prefix := asn1.ObjectIdentifier{1, 2, 3}

	in := Floats{
		F32: 3.1415927,
		F64: 2.718281828459045,
	}

	exts, err := Encode(prefix, in)
	require.NoError(t, err)

	var out Floats
	err = Decode(prefix, exts, &out)
	require.NoError(t, err)

	assert.Equal(t, in.F32, out.F32) //nolint:testifylint // this is exact
	assert.Equal(t, in.F64, out.F64) //nolint:testifylint // this is exact
}
