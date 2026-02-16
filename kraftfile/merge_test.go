// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package kraftfile

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMergeBasicFields(t *testing.T) {
	base := &Kraftfile{
		Name: "base-app",
		Cmd:  Command{"-c", "config.conf"},
	}

	current := &Kraftfile{}
	current.Merge(base)

	require.Equal(t, "base-app", current.Name)
	require.Equal(t, Command{"-c", "config.conf"}, current.Cmd)
}

func TestMergeOverrideFields(t *testing.T) {
	base := &Kraftfile{
		Name: "base-app",
		Cmd:  Command{"-c", "config.conf"},
	}

	current := &Kraftfile{
		Name: "my-app",
		Cmd:  Command{"--verbose"},
	}
	current.Merge(base)

	// other (base) takes precedence, so current is overridden
	require.Equal(t, "base-app", current.Name)
	require.Equal(t, Command{"-c", "config.conf"}, current.Cmd)
}

func TestMergeTargets(t *testing.T) {
	base := &Kraftfile{
		Targets: []Target{
			{Plat: "qemu", Arch: "x86_64"},
			{Plat: "xen", Arch: "arm64"},
		},
	}

	// Current with no targets should inherit from base
	current := &Kraftfile{}
	current.Merge(base)
	require.Len(t, current.Targets, 2)
	require.Equal(t, "qemu", current.Targets[0].Plat)
	require.Equal(t, "x86_64", current.Targets[0].Arch)

	// Current with targets should be overridden by base (other takes precedence)
	current = &Kraftfile{
		Targets: []Target{
			{Plat: "fc", Arch: "x86_64"},
		},
	}
	current.Merge(base)
	require.Len(t, current.Targets, 2)
	require.Equal(t, "qemu", current.Targets[0].Plat)
	require.Equal(t, "xen", current.Targets[1].Plat)
}

func TestMergeEnv(t *testing.T) {
	base := &Kraftfile{
		Env: Map{
			{Key: "HOME", Value: "/"},
			{Key: "PATH", Value: "/bin"},
		},
	}

	current := &Kraftfile{
		Env: Map{
			{Key: "HOME", Value: "/root"},
			{Key: "USER", Value: "root"},
		},
	}
	current.Merge(base)

	// Should have 3 items: HOME (overridden by base), PATH (from base), USER (from current)
	require.Len(t, current.Env, 3)
	require.Equal(t, "/", current.Env.Get("HOME")) // base takes precedence
	require.Equal(t, "/bin", current.Env.Get("PATH"))
	require.Equal(t, "root", current.Env.Get("USER"))
}

func TestMergeLabels(t *testing.T) {
	base := &Kraftfile{
		Labels: map[string]string{
			"app":  "nginx",
			"tier": "web",
		},
	}

	current := &Kraftfile{
		Labels: map[string]string{
			"app":     "custom",
			"version": "1.0",
		},
	}
	current.Merge(base)

	require.Len(t, current.Labels, 3)
	require.Equal(t, "nginx", current.Labels["app"]) // base takes precedence
	require.Equal(t, "web", current.Labels["tier"])
	require.Equal(t, "1.0", current.Labels["version"])
}

func TestMergeUnikraft(t *testing.T) {
	base := &Kraftfile{
		Unikraft: &Unikraft{
			Version: "stable",
			KConfig: Map{
				{Key: "CONFIG_LIBVFSCORE", Value: "y"},
			},
		},
	}

	// Current with no unikraft should inherit from base
	current := &Kraftfile{}
	current.Merge(base)
	require.NotNil(t, current.Unikraft)
	require.Equal(t, "stable", current.Unikraft.Version)
	require.Equal(t, "y", current.Unikraft.KConfig.Get("CONFIG_LIBVFSCORE"))

	// Current with partial unikraft should be overridden by base (other takes precedence)
	current = &Kraftfile{
		Unikraft: &Unikraft{
			Version: "staging",
		},
	}
	current.Merge(base)
	require.Equal(t, "stable", current.Unikraft.Version) // base takes precedence
	require.Equal(t, "y", current.Unikraft.KConfig.Get("CONFIG_LIBVFSCORE"))
}

func TestMergeLibraries(t *testing.T) {
	base := &Kraftfile{
		Libraries: map[string]Library{
			"musl": {Version: "stable"},
			"lwip": {Version: "stable"},
		},
	}

	current := &Kraftfile{
		Libraries: map[string]Library{
			"lwip":  {Version: "staging"},
			"redis": {Version: "stable"},
		},
	}
	current.Merge(base)

	require.Len(t, current.Libraries, 3)
	require.Equal(t, "stable", current.Libraries["musl"].Version)
	require.Equal(t, "stable", current.Libraries["lwip"].Version) // base takes precedence
	require.Equal(t, "stable", current.Libraries["redis"].Version)
}

func TestMergeRootfsAndVolumes(t *testing.T) {
	base := &Kraftfile{
		Rootfs: &FS{
			Source: "./base-rootfs",
			Format: FsTypeCpio,
		},
		Volumes: Volumes{
			{Source: "./data", Destination: "/data"},
		},
	}

	current := &Kraftfile{}
	current.Merge(base)

	require.NotNil(t, current.Rootfs)
	require.Equal(t, "./base-rootfs", current.Rootfs.Source)
	require.Len(t, current.Volumes, 1)
	require.Equal(t, "./data", current.Volumes[0].Source)
}

func TestMergeTemplateNotCarriedOver(t *testing.T) {
	base := &Kraftfile{
		Template: &Template{
			Source:  "https://github.com/unikraft/app-template.git",
			Version: "stable",
		},
	}

	current := &Kraftfile{}
	current.Merge(base)

	// Template should NOT be merged (avoid recursive templates)
	require.Nil(t, current.Template)
}

func TestMergeComplexExample(t *testing.T) {
	// This test mirrors the example from the v0.6 documentation
	base := &Kraftfile{
		Name: "template",
		Unikraft: &Unikraft{
			Version: "stable",
			KConfig: Map{
				{Key: "CONFIG_LIBVFSCORE", Value: "y"},
			},
		},
		Targets: []Target{
			{Plat: "qemu", Arch: "x86_64"},
		},
	}

	current := &Kraftfile{
		Unikraft: &Unikraft{
			Version: "staging",
		},
	}
	current.Merge(base)

	// With new precedence: other (base) always wins
	require.Equal(t, "template", current.Name)
	require.Equal(t, "stable", current.Unikraft.Version) // base takes precedence
	require.Len(t, current.Targets, 1)
	require.Equal(t, "qemu", current.Targets[0].Plat)
	require.Equal(t, "y", current.Unikraft.KConfig.Get("CONFIG_LIBVFSCORE"))
}
