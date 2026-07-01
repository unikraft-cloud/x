// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package oidext provides a structured, deterministic mapping between Go
// structs and ASN.1 OID-based X.509 extensions.
//
// It is intended for use with X.509 certificates and certificate signing
// requests (CSRs) where additional, application-specific metadata must be
// encoded as standards-compliant extensions.
//
// Each exported struct field is mapped to a unique Object Identifier (OID),
// constructed by appending a field-specific suffix (declared via struct tags)
// to a caller-supplied prefix OID. Field values are ASN.1 DER-encoded using
// Go’s encoding/asn1 package and emitted as pkix.Extension values.
//
// The package supports round-trip encoding and decoding, critical extensions,
// optional fields, nested structs, and deterministic extension ordering. It
// deliberately avoids implicit behavior and schema inference, favoring explicit
// OID assignment and strong typing.
//
// Typical use cases include private PKI extensions, CSR metadata injection,
// workload or host identity attributes, licensing claims, and platform-specific
// certificate annotations.
package oidext

import (
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
)

var (
	ErrNotStructPointer = errors.New("value must be a non-nil pointer to a struct")
	ErrNotStruct        = errors.New("value must be a struct or pointer to struct")
	ErrNotFieldPointer  = errors.New("value must be a non-nil pointer to a struct field")
	ErrFieldNotFound    = errors.New("value must point to a tagged field in the provided struct")
)

// Encode encodes exported struct fields into pkix.Extensions. Each exported
// field must have an `oid:"<suffix>"` tag unless WithEncodeIgnoreUntagged() is
// passed as an option.
//
// The full OID is prefixOID + suffixOID (concatenation of arcs).
//
// Tags:
//   - oid:"1.2.3" (suffix arcs)
//   - critical
//   - omitempty   (skip if zero value; pointers nil considered zero)
//
// Convenience combined tag form is also supported in `oid`:
//
//   - oid:"1.2.3,critical,omitempty"
func Encode(prefixOID asn1.ObjectIdentifier, v any, opts ...EncodeOption) ([]pkix.Extension, error) {
	cfg := defaultEncodeConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return nil, ErrNotStruct
	}
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, ErrNotStruct
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil, ErrNotStruct
	}

	rt := rv.Type()
	var exts []pkix.Extension

	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		if sf.PkgPath != "" {
			continue
		}

		tag := parseFieldTag(sf)
		if tag.skip {
			continue
		}

		if tag.inline {
			// Recurse into the embedded struct using the same prefix.
			// The field must be a struct or pointer to struct.
			fv := rv.Field(i)
			if fv.Kind() == reflect.Pointer {
				if fv.IsNil() {
					continue
				}
				fv = fv.Elem()
			}
			if fv.Kind() != reflect.Struct {
				return nil, fmt.Errorf("field %s: oid:\",inline\" requires a struct or pointer to struct", sf.Name)
			}
			// Make an addressable copy if needed so we can take its address.
			if !fv.CanAddr() {
				tmp := reflect.New(fv.Type()).Elem()
				tmp.Set(fv)
				fv = tmp
			}
			sub, err := Encode(prefixOID, fv.Addr().Interface(), opts...)
			if err != nil {
				return nil, fmt.Errorf("field %s (inline): %w", sf.Name, err)
			}
			exts = append(exts, sub...)
			continue
		}

		if len(tag.suffixOID) == 0 {
			if cfg.ignoreUntagged {
				continue
			}
			return nil, fmt.Errorf("field %s: missing oid tag", sf.Name)
		}

		fv := rv.Field(i)
		if tag.omitempty && isZeroValue(fv) {
			continue
		}

		der, err := marshalFieldValue(fv)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", sf.Name, err)
		}

		exts = append(exts, pkix.Extension{
			Id:       slices.Concat(prefixOID, tag.suffixOID),
			Critical: tag.critical,
			Value:    der,
		})
	}

	sort.Slice(exts, func(i, j int) bool {
		return compareOID(exts[i].Id, exts[j].Id) < 0
	})

	return exts, nil
}

// Decode populates out (pointer to struct) from pkix.Extensions. It matches
// extensions by full OID = prefixOID + suffixOID for each tagged field.
//
// Behavior:
//   - Unknown extensions are ignored (by default).
//   - Missing fields: if WithDecodeRequireAll() is used, missing required
//     fields error.  A field is considered "required" if it is not omitempty
//     and not a pointer.
func Decode(prefixOID asn1.ObjectIdentifier, exts []pkix.Extension, out any, opts ...DecodeOption) error {
	cfg := defaultDecodeConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	rv := reflect.ValueOf(out)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return ErrNotStructPointer
	}
	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return ErrNotStructPointer
	}

	extMap := make(map[string]pkix.Extension, len(exts))
	for _, e := range exts {
		extMap[e.Id.String()] = e
	}

	rt := rv.Type()

	for i := range rt.NumField() {
		sf := rt.Field(i)
		if sf.PkgPath != "" {
			continue
		}

		tag := parseFieldTag(sf)
		if tag.skip {
			continue
		}

		if tag.inline {
			// Recurse into the embedded struct using the same prefix and
			// the same extension map.
			fv := rv.Field(i)
			if fv.Kind() == reflect.Pointer {
				if fv.IsNil() {
					fv.Set(reflect.New(fv.Type().Elem()))
				}
				fv = fv.Elem()
			}
			if fv.Kind() != reflect.Struct {
				return fmt.Errorf("field %s: oid:\",inline\" requires a struct or pointer to struct", sf.Name)
			}
			if err := Decode(prefixOID, exts, fv.Addr().Interface(), opts...); err != nil {
				return fmt.Errorf("field %s (inline): %w", sf.Name, err)
			}
			continue
		}

		if len(tag.suffixOID) == 0 {
			continue
		}

		fullOID := slices.Concat(prefixOID, tag.suffixOID)
		ext, ok := extMap[fullOID.String()]
		if !ok {
			if cfg.requireAll && !tag.omitempty && sf.Type.Kind() != reflect.Pointer {
				return fmt.Errorf(
					"missing required extension for field %s (oid %s)",
					sf.Name,
					fullOID.String(),
				)
			}
			continue
		}

		if err := unmarshalIntoField(ext.Value, rv.Field(i)); err != nil {
			return fmt.Errorf("field %s: %w", sf.Name, err)
		}
	}

	return nil
}

// Inspect returns the full ASN.1 OID for a struct field, given the same
// prefixOID that would be passed to Encode or Decode.
//
// Constraints:
//   - structPtr must be a non-nil pointer to the struct that owns the field.
//   - fieldPtr must be a non-nil pointer to one of its exported fields.
//   - The field must carry an `oid:"<suffix>"` tag.
//
// Example:
//
//	type Attrs struct {
//	    Hostname string `oid:"1,critical"`
//	    Port     int    `oid:"2"`
//	}
//
//	var a Attrs
//	prefix := asn1.ObjectIdentifier{1, 2, 840, 113549}
//	oid, err := oidext.Inspect(prefix, &a, &a.Hostname)
//	// oid == {1, 2, 840, 113549, 1}
func Inspect(prefixOID asn1.ObjectIdentifier, structPtr any, fieldPtr any) (asn1.ObjectIdentifier, error) {
	oids, err := Inspects(prefixOID, structPtr, fieldPtr)
	if err != nil {
		return nil, err
	}
	return oids[0], nil
}

// Inspects returns the full ASN.1 OID for each of the given struct fields, in
// the same order as fieldPtrs. It behaves exactly like calling Inspect once per
// field pointer, but walks the struct a single time.
//
// Constraints:
//   - structPtr must be a non-nil pointer to the struct that owns the fields.
//   - every fieldPtr must be a non-nil pointer to one of its exported fields.
//   - each field must carry an `oid:"<suffix>"` tag.
//
// If any fieldPtr does not resolve to a tagged field, Inspects returns
// ErrFieldNotFound.
func Inspects(prefixOID asn1.ObjectIdentifier, structPtr any, fieldPtrs ...any) ([]asn1.ObjectIdentifier, error) {
	structBase, sv, err := structBaseValue(structPtr)
	if err != nil {
		return nil, err
	}

	// Resolve each requested field pointer to its address, keyed by address so
	// a single walk can satisfy them all. Distinct fields have distinct
	// addresses; duplicate pointers simply map to the same result slot.
	wanted := make(map[uintptr][]int, len(fieldPtrs))
	for i, fieldPtr := range fieldPtrs {
		fv := reflect.ValueOf(fieldPtr)
		if fv.Kind() != reflect.Pointer || fv.IsNil() {
			return nil, ErrNotFieldPointer
		}
		addr := fv.Pointer()
		wanted[addr] = append(wanted[addr], i)
	}

	out := make([]asn1.ObjectIdentifier, len(fieldPtrs))
	found := make([]bool, len(fieldPtrs))

	err = walkFields(prefixOID, structBase, sv, func(fieldAddr uintptr, oid asn1.ObjectIdentifier, sf reflect.StructField) (bool, error) {
		idxs, ok := wanted[fieldAddr]
		if !ok {
			return false, nil
		}
		if len(oid) == 0 {
			return true, fmt.Errorf("field %s: missing oid tag", sf.Name)
		}
		for _, idx := range idxs {
			out[idx] = oid
			found[idx] = true
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}

	for _, ok := range found {
		if !ok {
			return nil, ErrFieldNotFound
		}
	}
	return out, nil
}

// All returns the full ASN.1 OID for every tagged, exported field of the
// struct, in struct declaration order (recursing into inline structs). Unlike
// Inspect, it takes no field pointers: it reports the complete OID set that
// Encode would consider for the struct.
//
// structPtr may be a struct or a pointer to a struct.
func All(prefixOID asn1.ObjectIdentifier, structPtr any) ([]asn1.ObjectIdentifier, error) {
	structBase, sv, err := structBaseValue(structPtr)
	if err != nil {
		return nil, err
	}

	var out []asn1.ObjectIdentifier
	err = walkFields(prefixOID, structBase, sv, func(_ uintptr, oid asn1.ObjectIdentifier, sf reflect.StructField) (bool, error) {
		if len(oid) == 0 {
			return false, nil
		}
		out = append(out, oid)
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func structBaseValue(structPtr any) (uintptr, reflect.Value, error) {
	sv := reflect.ValueOf(structPtr)
	if !sv.IsValid() {
		return 0, reflect.Value{}, ErrNotStructPointer
	}

	var base uintptr
	if sv.Kind() == reflect.Pointer {
		if sv.IsNil() {
			return 0, reflect.Value{}, ErrNotStructPointer
		}
		base = sv.Pointer()
		sv = sv.Elem()
	}
	if sv.Kind() != reflect.Struct {
		return 0, reflect.Value{}, ErrNotStructPointer
	}
	return base, sv, nil
}

func walkFields(
	prefixOID asn1.ObjectIdentifier,
	structBase uintptr,
	sv reflect.Value,
	visit func(fieldAddr uintptr, oid asn1.ObjectIdentifier, sf reflect.StructField) (bool, error),
) error {
	rt := sv.Type()
	for i := range rt.NumField() {
		sf := rt.Field(i)
		if sf.PkgPath != "" {
			continue
		}

		tag := parseFieldTag(sf)
		if tag.skip {
			continue
		}

		fv := sv.Field(i)

		if tag.inline {
			// For a value-embedded struct the child's address is
			// structBase + offset. For a pointer-embedded struct that offset
			// locates the pointer word, not the pointee, so take the real
			// address the pointer holds instead.
			base := structBase + sf.Offset
			if fv.Kind() == reflect.Pointer {
				if fv.IsNil() {
					continue
				}
				base = fv.Pointer()
				fv = fv.Elem()
			}
			if fv.Kind() != reflect.Struct {
				continue
			}
			if err := walkFields(prefixOID, base, fv, visit); err != nil {
				return fmt.Errorf("field %s (inline): %w", sf.Name, err)
			}
			continue
		}

		fullOID := tag.suffixOID
		if len(fullOID) != 0 {
			fullOID = slices.Concat(prefixOID, tag.suffixOID)
		}

		stop, err := visit(structBase+sf.Offset, fullOID, sf)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}

	return nil
}

type fieldTag struct {
	suffixOID asn1.ObjectIdentifier
	critical  bool
	omitempty bool
	skip      bool
	inline    bool
}

func parseFieldTag(sf reflect.StructField) fieldTag {
	var ft fieldTag

	raw := strings.TrimSpace(sf.Tag.Get("oid"))
	if raw == "-" {
		ft.skip = true
		return ft
	}

	var (
		seenCritical  bool
		seenOmitEmpty bool
	)

	if raw != "" {
		parts := splitCSVLike(raw)
		if len(parts) > 0 {
			oidPart := strings.TrimSpace(parts[0])
			if oidPart != "" {
				soid, err := parseOID(oidPart)
				if err == nil {
					ft.suffixOID = soid
				}
			}
		}

		for _, p := range parts[1:] {
			switch strings.ToLower(strings.TrimSpace(p)) {
			case "critical":
				ft.critical = true
				seenCritical = true
			case "omitempty":
				ft.omitempty = true
				seenOmitEmpty = true
			case "inline":
				ft.inline = true
			}
		}
	}

	// Legacy fallback tags (only if not explicitly set in oid tag)
	if !seenCritical && isTruthy(sf.Tag.Get("critical")) {
		ft.critical = true
	}
	if !seenOmitEmpty && isTruthy(sf.Tag.Get("omitempty")) {
		ft.omitempty = true
	}

	return ft
}

func splitCSVLike(s string) []string {
	// Simple split: no quoted commas supported (good enough for our tags).
	raw := strings.Split(s, ",")
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		out = append(out, strings.TrimSpace(r))
	}
	return out
}

func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func parseOID(dotted string) (asn1.ObjectIdentifier, error) {
	dotted = strings.TrimSpace(dotted)
	if dotted == "" {
		return nil, errors.New("empty oid")
	}
	arcs := strings.Split(dotted, ".")
	oid := make(asn1.ObjectIdentifier, 0, len(arcs))
	for _, a := range arcs {
		a = strings.TrimSpace(a)
		if a == "" {
			return nil, fmt.Errorf("invalid oid: %q", dotted)
		}

		n, err := strconv.Atoi(a)
		if err != nil {
			return nil, fmt.Errorf("invalid oid arc %q in %q", a, dotted)
		}

		oid = append(oid, n)
	}
	return oid, nil
}

func compareOID(a, b asn1.ObjectIdentifier) int {
	for i := range min(len(a), len(b)) {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}

func marshalFieldValue(v reflect.Value) ([]byte, error) {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, errors.New("nil pointer")
		}
		v = v.Elem()
	}

	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil, errors.New("nil interface")
		}
		v = v.Elem()
	}

	// OID attributes do not support float-types.  As a workaround, we handle
	// them specially by encoding their bit-pattern as a byte-slice.
	switch v.Kind() {
	case reflect.Float32:
		bits := math.Float32bits(float32(v.Float()))
		buf := make([]byte, 4)
		binary.BigEndian.PutUint32(buf, bits)
		return asn1.Marshal(buf)

	case reflect.Float64:
		bits := math.Float64bits(v.Float())
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, bits)
		return asn1.Marshal(buf)

	default:
		return asn1.Marshal(v.Interface())
	}
}

func unmarshalIntoField(der []byte, field reflect.Value) error {
	if !field.CanSet() {
		return errors.New("field cannot be set")
	}

	if field.Kind() == reflect.Pointer {
		if field.IsNil() {
			field.Set(reflect.New(field.Type().Elem()))
		}
		return unmarshalIntoField(der, field.Elem())
	}

	switch field.Kind() {
	case reflect.Float32:
		var buf []byte
		rest, err := asn1.Unmarshal(der, &buf)
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return errors.New("trailing data")
		}
		if len(buf) != 4 {
			return fmt.Errorf("invalid float32 length: %d", len(buf))
		}
		bits := binary.BigEndian.Uint32(buf)
		field.SetFloat(float64(math.Float32frombits(bits)))
		return nil

	case reflect.Float64:
		var buf []byte
		rest, err := asn1.Unmarshal(der, &buf)
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return errors.New("trailing data")
		}
		if len(buf) != 8 {
			return fmt.Errorf("invalid float64 length: %d", len(buf))
		}
		bits := binary.BigEndian.Uint64(buf)
		field.SetFloat(math.Float64frombits(bits))
		return nil
	}

	tmpPtr := reflect.New(field.Type())
	rest, err := asn1.Unmarshal(der, tmpPtr.Interface())
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New("trailing data")
	}

	field.Set(tmpPtr.Elem())
	return nil
}

func isZeroValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Slice, reflect.Map, reflect.Func:
		return v.IsNil()
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if !isZeroValue(v.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Struct:
		// reflect.Value.IsZero exists in modern Go
		return v.IsZero()
	default:
		return v.IsZero()
	}
}
