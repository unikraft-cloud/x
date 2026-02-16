// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package kraftfile

import (
	"maps"
	"slices"
)

// Merge applies the provided Kraftfile on top of the current Kraftfile.
// Fields from the other Kraftfile will always take precedence and overwrite
// existing values in the current Kraftfile.
func (kf *Kraftfile) Merge(other *Kraftfile) {
	// Name: other always takes precedence
	if other.Name != "" {
		kf.Name = other.Name
	}

	// Targets: other always takes precedence
	if len(other.Targets) > 0 {
		kf.Targets = slices.Clone(other.Targets)
	}

	// Cmd: other always takes precedence
	if len(other.Cmd) > 0 {
		kf.Cmd = slices.Clone(other.Cmd)
	}

	// Env: Merge maps with other taking precedence
	if len(other.Env) > 0 {
		if kf.Env == nil {
			kf.Env = make(Map, len(other.Env))
		}
		kf.Env.Merge(other.Env)
	}

	// Labels: Merge maps with other taking precedence
	if len(other.Labels) > 0 {
		if kf.Labels == nil {
			kf.Labels = make(map[string]string)
		}
		maps.Copy(kf.Labels, other.Labels)
	}

	// Runtime: other always takes precedence
	if other.Runtime != nil {
		kf.Runtime = other.Runtime
	}

	// Unikraft: Merge unikraft configuration with other taking precedence
	if other.Unikraft != nil {
		if kf.Unikraft == nil {
			// Use other entirely if current has no unikraft config
			unikraftCopy := *other.Unikraft
			kf.Unikraft = &unikraftCopy
			if len(other.Unikraft.KConfig) > 0 {
				kf.Unikraft.KConfig = slices.Clone(other.Unikraft.KConfig)
			}
		} else {
			// Merge fields: other always takes precedence
			if other.Unikraft.Source != "" {
				kf.Unikraft.Source = other.Unikraft.Source
			}
			if other.Unikraft.Version != "" {
				kf.Unikraft.Version = other.Unikraft.Version
			}
			// Merge KConfig with other taking precedence
			kf.Unikraft.KConfig.Merge(other.Unikraft.KConfig)
		}
	}

	// Libraries: Merge libraries with other taking precedence
	if len(other.Libraries) > 0 {
		if kf.Libraries == nil {
			kf.Libraries = make(map[string]Library)
		}
		maps.Copy(kf.Libraries, other.Libraries)
	}

	// Rootfs: other always takes precedence
	if other.Rootfs != nil {
		kf.Rootfs = other.Rootfs
	}

	// Roms: other always takes precedence
	if len(other.Roms) > 0 {
		kf.Roms = slices.Clone(other.Roms)
	}

	// Volumes: other always takes precedence
	if len(other.Volumes) > 0 {
		kf.Volumes = slices.Clone(other.Volumes)
	}

	// OutDir: other always takes precedence
	if other.OutDir != "" {
		kf.OutDir = other.OutDir
	}

	// Template: Explicitly do not merge template (avoid recursive templates)
}

func (current *Map) Merge(other Map) {
	if len(other) == 0 {
		return
	}

	ks := make(map[string]int, len(*current))
	for i, kv := range *current {
		ks[kv.Key] = i
	}

	for _, curr := range other {
		if i, ok := ks[curr.Key]; ok {
			(*current)[i] = curr
		} else {
			*current = append(*current, curr)
		}
	}
}
