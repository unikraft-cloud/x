// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package oidext

import (
	"crypto/x509/pkix"
	"encoding/asn1"
	"reflect"
	"testing"
	"time"
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
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Ensure Hostname extension is present and critical
	wantHostnameOID := append(prefix, 1)
	found := false
	for _, e := range exts {
		if e.Id.String() == wantHostnameOID.String() {
			found = true
			if !e.Critical {
				t.Fatalf("expected hostname extension to be critical")
			}
		}
	}
	if !found {
		t.Fatalf("did not find hostname extension")
	}

	var out HostAttrs
	if err := Decode(prefix, exts, &out, WithDecodeIgnoreUnknown()); err != nil {
		t.Fatalf("Decode: %v", err)
	}

	// SkipMe is untagged via "-", should remain zero
	if out.SkipMe != "" {
		t.Fatalf("expected SkipMe to be empty, got %q", out.SkipMe)
	}

	// Compare values (time.Time and pointer included)
	if !reflect.DeepEqual(in.Hostname, out.Hostname) ||
		!reflect.DeepEqual(in.Fingerprint, out.Fingerprint) ||
		in.BootCount != out.BootCount ||
		in.Enabled != out.Enabled ||
		!reflect.DeepEqual(in.Tags, out.Tags) ||
		!in.When.Equal(out.When) ||
		!reflect.DeepEqual(in.Inner, out.Inner) ||
		!reflect.DeepEqual(in.Embedded, out.Embedded) ||
		(out.Optional == nil || *out.Optional != *in.Optional) {
		t.Fatalf("roundtrip mismatch:\n in=%#v\nout=%#v", in, out)
	}
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
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Optional (suffix 8) should not be present
	optionalOID := append(prefix, 8)
	for _, e := range exts {
		if e.Id.String() == optionalOID.String() {
			t.Fatalf("did not expect optional extension to be present")
		}
	}

	// Decode should leave Optional nil
	var out HostAttrs
	if err := Decode(prefix, exts, &out); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if out.Optional != nil {
		t.Fatalf("expected out.Optional nil")
	}
}

func TestRequireAll(t *testing.T) {
	prefix := asn1.ObjectIdentifier{1, 2, 3}

	// Only include one extension
	only := []pkix.Extension{
		{Id: append(prefix, 1), Critical: true, Value: mustDER(t, "node")},
	}

	var out HostAttrs
	err := Decode(prefix, only, &out, WithDecodeRequireAll())
	if err == nil {
		t.Fatalf("expected error due to missing required fields")
	}
}

func TestIgnoreUntagged(t *testing.T) {
	type WithExtra struct {
		A string `oid:"1"`
		B string // untagged
	}

	prefix := asn1.ObjectIdentifier{9, 9, 9}
	in := WithExtra{A: "x", B: "y"}

	_, err := Encode(prefix, in)
	if err == nil {
		t.Fatalf("expected error for missing oid tag when IgnoreUntagged=false")
	}

	exts, err := Encode(prefix, in, WithEncodeIgnoreUntagged())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(exts) != 1 {
		t.Fatalf("expected 1 extension, got %d", len(exts))
	}
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
				if err == nil {
					t.Fatalf("expected error, got nil (oid=%v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.String() != tc.wantOID.String() {
				t.Fatalf("OID mismatch: got %v, want %v", got, tc.wantOID)
			}
		})
	}
}

func mustDER(t *testing.T, s string) []byte {
	t.Helper()
	der, err := asn1.Marshal(s)
	if err != nil {
		t.Fatalf("asn1.Marshal: %v", err)
	}
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
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	var out Floats
	if err := Decode(prefix, exts, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if in.F32 != out.F32 {
		t.Fatalf("float32 mismatch: %v vs %v", in.F32, out.F32)
	}
	if in.F64 != out.F64 {
		t.Fatalf("float64 mismatch: %v vs %v", in.F64, out.F64)
	}
}
