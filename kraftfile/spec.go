// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package kraftfile

import (
	"fmt"
)

// Kraftfile represents a parsed Kraftfile
type Kraftfile struct {
	Spec string `json:"-"`

	Template *Template `json:"template,omitempty"`

	Name    string            `json:"name,omitempty"`
	Targets []Target          `json:"targets,omitempty"`
	Cmd     Command           `json:"cmd,omitempty"`
	Env     Map               `json:"env,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`

	Runtime   *Runtime           `json:"runtime,omitempty"`
	Unikraft  *Unikraft          `json:"unikraft,omitempty"`
	Libraries map[string]Library `json:"libraries,omitempty"`
	Rootfs    *FS                `json:"rootfs,omitempty"`
	Roms      []FS               `json:"roms,omitempty"`
	Volumes   Volumes            `json:"volumes,omitempty"`

	// Deprecated: OutDir is no longer supported.
	OutDir string `json:"outdir,omitempty"`
}

type Command []string

// Template extends a Kraftfile with a reference to a template, which can be used to
// populate the Kraftfile with additional content from the template.
type Template struct {
	// Source specifies the source of the template, which should be a reference
	// to a location which contains a Kraftfile.
	Source string `json:"source,omitempty"`

	// Version can be used to specify a version of the template, e.g. to be
	// used when referencing a remote git source.
	Version string `json:"version,omitempty"`
}

// Runtime is used to access a pre-built unikernel. The source must be specified
// as a path to an OCI image.
type Runtime string

// Unikraft defines the Unikraft component, which is used to build a unikernel
// from source.
type Unikraft struct {
	// Source specifies the source of the Unikraft component, which should be a
	// reference to a location which contains the unikraft source code
	// (upstream available at https://github.com/unikraft/unikraft.git).
	Source string `json:"source,omitempty"`

	// Version can be used to specify a version of the Unikraft component, e.g.
	// to be used when referencing a remote git source.
	Version string `json:"version,omitempty"`

	// KConfig can be used to specify additional KConfig options to be applied
	// when building the unikernel.
	KConfig Map `json:"kconfig,omitempty"`
}

// Library defines a library component, which is used to build a library into
// the unikernel from source.
type Library struct {
	// Source specifies the source of the library, which should be a reference
	// to a location which contains the library source code.
	Source string `json:"source,omitempty"`

	// Version can be used to specify a version of the library, e.g. to be
	// used when referencing a remote git source.
	Version string `json:"version,omitempty"`

	// KConfig can be used to specify additional KConfig options to be applied
	// when building the library.
	KConfig Map `json:"kconfig,omitempty"`
}

// FS defines a filesystem for the unikernel, which can be used to provide
// additional files to the unikernel at runtime.
type FS struct {
	// Source defines the source of the fs, which can be:
	// - a directory containing files to be included in the fs
	// - a tarball containing files to be included in the fs
	// - an existing packed fs
	// - a dockerfile which can be used to build a fs image
	Source string `json:"source,omitempty"`

	// Format specifies the output format of the fs image.
	Format FsType `json:"format,omitempty"`

	// Type specifies the type of the source for the fs.
	Type SourceType `json:"type,omitempty"`
}

type FsType string

const (
	FsTypeCpio  = FsType("cpio")
	FsTypeErofs = FsType("erofs")
)

func (fsType FsType) String() string {
	return string(fsType)
}

type SourceType string

const (
	SourceTypeOCI        = SourceType("oci")
	SourceTypeDirectory  = SourceType("dir")
	SourceTypeFile       = SourceType("file")
	SourceTypeTarball    = SourceType("tarball")
	SourceTypeCpio       = SourceType("cpio")
	SourceTypeErofs      = SourceType("erofs")
	SourceTypeDockerfile = SourceType("dockerfile")
)

func (sourceType SourceType) String() string {
	return string(sourceType)
}

// Volumes supports a string or list of volume entries.
type Volumes []Volume

type Volume struct {
	Driver      string `json:"driver,omitempty"`
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination,omitempty"`
	Mode        any    `json:"mode,omitempty"`
	ReadOnly    bool   `json:"readonly,omitempty"`
}

// Target supports shorthand or structured target entries.
type Target struct {
	Arch    string `json:"arch,omitempty"`
	Plat    string `json:"plat,omitempty"`
	KConfig Map    `json:"kconfig,omitempty"`
}

// Map stores ordered key/value pairs from list or map inputs.
type Map []MapPair

// MapPair represents a single key/value pair.
type MapPair struct {
	Key   string
	Value any
}

// Get returns the value for a key or nil if missing.
func (m Map) Get(key string) any {
	value, _ := m.Lookup(key)
	return value
}

// Lookup returns the value and whether the key exists.
func (m Map) Lookup(key string) (any, bool) {
	for _, item := range m {
		if item.Key == key {
			return item.Value, true
		}
	}
	return nil, false
}

func (m Map) AsMap() map[string]any {
	result := make(map[string]any, len(m))
	for _, item := range m {
		result[item.Key] = item.Value
	}
	return result
}

func (m Map) AsStringMap() map[string]string {
	result := make(map[string]string, len(m))
	for _, item := range m {
		var valueStr string
		switch v := item.Value.(type) {
		case string:
			valueStr = v
		case nil:
			valueStr = ""
		default:
			valueStr = fmt.Sprint(v)
		}
		result[item.Key] = valueStr
	}
	return result
}
