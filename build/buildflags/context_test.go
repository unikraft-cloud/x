// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026, The buildx authors.
// Licensed under the Apache License, Version 2.0 (the "License").
// You may not use this file except in compliance with the License.

// original code imported from github.com/docker/buildx/util/buildflags/

package buildflags

import (
	"reflect"
	"testing"
)

func TestParseContextNames(t *testing.T) {
	tests := []struct {
		name    string
		values  []string
		want    map[string]string
		wantErr bool
	}{
		{
			name:   "empty",
			values: nil,
			want:   nil,
		},
		{
			name:   "local directory",
			values: []string{"shared=./shared"},
			want:   map[string]string{"shared": "./shared"},
		},
		{
			name:   "image reference",
			values: []string{"docker.io/library/alpine:latest=alpine:latest"},
			want:   map[string]string{"alpine": "alpine:latest"},
		},
		{
			name:   "multiple contexts",
			values: []string{"shared=./shared", "base=alpine:latest"},
			want: map[string]string{
				"shared": "./shared",
				"base":   "alpine:latest",
			},
		},
		{
			name:   "source containing equals",
			values: []string{"config=./config?token=a=b"},
			want:   map[string]string{"config": "./config?token=a=b"},
		},
		{
			name:    "missing equals",
			values:  []string{"shared"},
			wantErr: true,
		},
		{
			name:    "empty name",
			values:  []string{"=./shared"},
			wantErr: true,
		},
		{
			name:    "empty source",
			values:  []string{"shared="},
			wantErr: true,
		},
		{
			name:    "duplicate name",
			values:  []string{"shared=./one", "shared=./two"},
			wantErr: true,
		},
		{
			name:    "duplicate normalized name",
			values:  []string{"alpine=./one", "alpine:latest=./two"},
			wantErr: true,
		},
		{
			name:    "reserved context name",
			values:  []string{"context=./shared"},
			wantErr: true,
		},
		{
			name:    "reserved dockerfile name",
			values:  []string{"dockerfile=./shared"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseContextNames(tt.values)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseContextNames() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseContextNames() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
