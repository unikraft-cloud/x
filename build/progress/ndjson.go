// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package progress

import (
	"encoding/json"
	"io"
	"slices"

	"github.com/moby/buildkit/client"
)

// Encoder writes statuses as newline-delimited JSON, one per line, in the
// shape of `docker buildx build --progress=rawjson`: buildkit's own JSON
// encoding of a SolveStatus.
type Encoder struct {
	enc *json.Encoder
}

func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{enc: json.NewEncoder(w)}
}

// Encode writes ss as one line.
func (e *Encoder) Encode(ss *client.SolveStatus) error {
	return e.enc.Encode(ss)
}

// Decoder reads the statuses an Encoder, or buildx, wrote.
type Decoder struct {
	dec *json.Decoder
}

// NewDecoder returns a Decoder that reads from r.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{dec: json.NewDecoder(r)}
}

// Decode reads the next status
func (d *Decoder) Decode() (*client.SolveStatus, error) {
	var ss client.SolveStatus
	if err := d.dec.Decode(&ss); err != nil {
		return nil, err
	}

	ss.Vertexes = slices.DeleteFunc(ss.Vertexes, isNil)
	ss.Statuses = slices.DeleteFunc(ss.Statuses, isNil)
	ss.Logs = slices.DeleteFunc(ss.Logs, isNil)
	ss.Warnings = slices.DeleteFunc(ss.Warnings, isNil)

	return &ss, nil
}

func isNil[T any](p *T) bool { return p == nil }
