// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package kraftfile

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
	"mvdan.cc/sh/v3/shell"
	"sigs.k8s.io/yaml"
)

const (
	SpecVersionMin = "v0.7"
	SpecVersionMax = "v0.7"
)

type parseOpts struct {
	skipVersionCheck bool
}

type ParseOpt func(*parseOpts)

// WithSkippedVersionCheck returns a ParseOpt that disables spec version checking.
func WithSkippedVersionCheck() ParseOpt {
	return func(opts *parseOpts) {
		opts.skipVersionCheck = true
	}
}

// ParseBytes parses a Kraftfile from bytes and validates the spec version.
func ParseBytes(data []byte, opts ...ParseOpt) (*Kraftfile, error) {
	opt := &parseOpts{}
	for _, o := range opts {
		o(opt)
	}

	var header struct {
		Spec          string `json:"spec,omitempty"`
		Specification string `json:"specification,omitempty"`
	}
	if err := yaml.Unmarshal(data, &header); err != nil {
		return nil, err
	}
	spec, err := normalizeSpec(cmp.Or(header.Spec, header.Specification))
	if err != nil {
		return nil, err
	}
	if !opt.skipVersionCheck {
		if semver.Compare(spec, SpecVersionMin) < 0 || semver.Compare(spec, SpecVersionMax) > 0 {
			return nil, fmt.Errorf("unsupported spec version %q", spec)
		}
	}

	var kf Kraftfile
	if err := yaml.Unmarshal(data, &kf); err != nil {
		return nil, err
	}
	kf.Spec = spec
	return &kf, nil
}

func normalizeSpec(spec string) (string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", fmt.Errorf("missing 'spec' version attribute")
	}
	if !strings.HasPrefix(spec, "v") {
		spec = "v" + spec
	}
	if !semver.IsValid(spec) {
		return "", fmt.Errorf("invalid spec version %q", spec)
	}
	return semver.MajorMinor(spec), nil
}

func (cmd *Command) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	switch value := raw.(type) {
	case nil:
		return nil
	case string:
		fields, err := shell.Fields(value, func(field string) string { return "" })
		if err != nil {
			return fmt.Errorf("could not parse cmd string: %w", err)
		}
		*cmd = fields
		return nil
	case []any:
		list := make([]string, 0, len(value))
		for _, item := range value {
			str, ok := item.(string)
			if !ok {
				return fmt.Errorf("cmd array entries must be strings")
			}
			list = append(list, str)
		}
		*cmd = list
		return nil
	default:
		return fmt.Errorf("invalid cmd value type %T", raw)
	}
}

func (m *Map) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	switch value := raw.(type) {
	case nil:
		return nil
	case []any:
		items := make([]MapPair, 0, len(value))
		for _, item := range value {
			str, ok := item.(string)
			if !ok {
				return fmt.Errorf("map list entries must be strings")
			}
			key, val := splitMapPair(str)
			items = append(items, MapPair{Key: key, Value: val})
		}
		*m = items
		return nil
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		items := make([]MapPair, 0, len(keys))
		for _, key := range keys {
			value := value[key]
			switch value.(type) {
			case string, float64, bool, nil:
			default:
				return fmt.Errorf("map values must be scalar")
			}
			items = append(items, MapPair{Key: key, Value: value})
		}
		*m = items
		return nil
	default:
		return fmt.Errorf("invalid map value type %T", raw)
	}
}

func splitMapPair(value string) (string, any) {
	parts := strings.SplitN(value, "=", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return value, nil
}

func (ref *Runtime) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	switch value := raw.(type) {
	case nil:
		return nil
	case string:
		*ref = Runtime(value)
		return nil
	default:
		return fmt.Errorf("invalid runtime value type %T", raw)
	}
}

func (ref *Unikraft) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	switch value := raw.(type) {
	case nil:
		return nil
	case string:
		ref.Version = fmt.Sprint(value)
		return nil
	case map[string]any:
		type alias Unikraft
		return json.Unmarshal(data, (*alias)(ref))
	default:
		return fmt.Errorf("invalid unikraft value type %T", raw)
	}
}

func (ref *Library) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	switch value := raw.(type) {
	case nil:
		return nil
	case string:
		ref.Version = fmt.Sprint(value)
		return nil
	case map[string]any:
		type alias Library
		return json.Unmarshal(data, (*alias)(ref))
	default:
		return fmt.Errorf("invalid library value type %T", raw)
	}
}

func (ref *Template) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	switch value := raw.(type) {
	case nil:
		return nil
	case string:
		ref.Source = value
		return nil
	case map[string]any:
		type alias Template
		return json.Unmarshal(data, (*alias)(ref))
	default:
		return fmt.Errorf("invalid template value type %T", raw)
	}
}

func (fs *FS) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	switch val := raw.(type) {
	case nil:
		return nil
	case string:
		var path string
		if err := json.Unmarshal(data, &path); err != nil {
			return err
		}
		fs.Source = &FSSource{Path: path}
		return nil
	case map[string]any:
		// Use a helper struct so we can inspect the raw source value before
		// deciding whether it is a string (deprecated) or an FSSource object.
		var tmp struct {
			Source json.RawMessage `json:"source,omitempty"`
			Format FsType          `json:"format,omitempty"`
		}
		if err := json.Unmarshal(data, &tmp); err != nil {
			return err
		}
		fs.Format = tmp.Format

		if tmp.Source == nil {
			return fmt.Errorf("'source' is required")
		}

		var sourceRaw any
		if err := json.Unmarshal(tmp.Source, &sourceRaw); err != nil {
			return err
		}
		switch s := sourceRaw.(type) {
		case string:
			// Deprecated: source as a plain string path. Kept for backward
			// compatibility. Also pick up the legacy top-level type field.
			src := &FSSource{Path: s}
			var legacy struct {
				Type SourceType `json:"type,omitempty"`
			}
			if err := json.Unmarshal(data, &legacy); err != nil {
				return err
			}
			src.Type = legacy.Type
			fs.Source = src
		case map[string]any:
			// Reject the legacy top-level type field when source is already an
			// object — it would be silently ignored, which is confusing.
			if _, hasTopLevelType := val["type"]; hasTopLevelType {
				return fmt.Errorf("top-level 'type' is not allowed when 'source' is an object; use 'source.type' instead")
			}
			var src FSSource
			if err := json.Unmarshal(tmp.Source, &src); err != nil {
				return err
			}
			fs.Source = &src
		default:
			return fmt.Errorf("invalid source value type %T", sourceRaw)
		}
		return nil
	default:
		return fmt.Errorf("invalid fs value type %T", raw)
	}
}

func (src *FSSource) UnmarshalJSON(data []byte) error {
	type alias FSSource
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	// Reject an explicitly-set type that contradicts the dockerfile field.
	if a.Dockerfile != "" && a.Type != "" && a.Type != SourceTypeDockerfile {
		return fmt.Errorf("type must be %q when dockerfile is set, got %q", SourceTypeDockerfile, a.Type)
	}
	// When dockerfile is set and type is omitted, infer type as "dockerfile".
	if a.Dockerfile != "" && a.Type == "" {
		a.Type = SourceTypeDockerfile
	}
	*src = FSSource(a)
	return nil
}

func (fsType *FsType) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	switch value := raw.(type) {
	case nil:
		return nil
	case string:
		if !slices.Contains(FsTypes, FsType(value)) {
			return fmt.Errorf("invalid fs type %q", value)
		}
		*fsType = FsType(value)
		return nil
	default:
		return fmt.Errorf("invalid fs value type %T", raw)
	}
}

func (srcType *SourceType) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	switch value := raw.(type) {
	case nil:
		return nil
	case string:
		if !slices.Contains(SourceTypes, SourceType(value)) {
			return fmt.Errorf("invalid source type %q", value)
		}
		*srcType = SourceType(value)
		return nil
	default:
		return fmt.Errorf("invalid source value type %T", raw)
	}
}

func (volumes *Volumes) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	switch raw.(type) {
	case nil:
		return nil
	case []any:
		var list []Volume
		if err := json.Unmarshal(data, &list); err != nil {
			return err
		}
		*volumes = list
		return nil
	default:
		return fmt.Errorf("invalid volumes value type %T", raw)
	}
}

func (volume *Volume) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	switch value := raw.(type) {
	case nil:
		return nil
	case string:
		if err := volume.UnmarshalText([]byte(value)); err != nil {
			return err
		}
		return nil
	case map[string]any:
		type alias Volume
		return json.Unmarshal(data, (*alias)(volume))
	default:
		return fmt.Errorf("invalid volume value type %T", raw)
	}
}

func (target *Target) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	switch value := raw.(type) {
	case nil:
		return nil
	case string:
		return parseTargetString(value, target)
	case map[string]any:
		type alias Target
		if err := json.Unmarshal(data, (*alias)(target)); err != nil {
			return err
		}
		var rawMap map[string]any
		if err := json.Unmarshal(data, &rawMap); err != nil {
			return err
		}
		if target.Arch == "" {
			if value, ok := rawMap["architecture"]; ok {
				arch, ok := value.(string)
				if !ok {
					return fmt.Errorf("architecture must be a string")
				}
				target.Arch = arch
			}
		}
		if target.Plat == "" {
			if value, ok := rawMap["platform"]; ok {
				plat, ok := value.(string)
				if !ok {
					return fmt.Errorf("platform must be a string")
				}
				target.Plat = plat
			}
		}
		return nil
	default:
		return fmt.Errorf("invalid target value type %T", raw)
	}
}

func parseTargetString(value string, target *Target) error {
	plat, arch, ok := strings.Cut(value, "/")
	if !ok {
		return fmt.Errorf("invalid target string %q: expected format 'platform/architecture'", value)
	}
	target.Plat = plat
	target.Arch = arch
	return nil
}

func (volume *Volume) UnmarshalText(text []byte) error {
	entry := string(text)
	parts := strings.SplitN(entry, ":", 3)
	if len(parts) == 1 {
		// When no colon is specified, assume the root file system
		volume.Source = entry
		volume.Destination = "/"
		return nil
	}
	volume.Source = parts[0]
	volume.Destination = parts[1]
	if len(parts) == 3 {
		volume.Mode = parts[2]
	}
	return nil
}
