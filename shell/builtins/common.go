// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package builtins

// Format is how a caller is asked to print what it was asked for. The names
// are the shell's; what they mean is the caller's to decide.
type Format struct {
	Field  []string `short:"f" help:"Fields to include in the output."`
	Output string   `short:"o" help:"Output format. One of: kv, table, json, yaml, raw, quiet, template."`
}

// MountArgs attaches a volume to the instance.
type MountArgs struct {
	Volume   string `arg:"" completion-predictor:"resource-key-volume" help:"Volume to attach."`
	At       string `arg:"" name:"path" help:"Absolute mount path inside the instance."`
	Readonly bool   `help:"Mount the volume as read-only."`
}

// UnmountArgs detaches a volume from the instance.
type UnmountArgs struct {
	Volume string `arg:"" completion-predictor:"resource-key-volume" help:"Volume to detach."`
}

// EditArgs changes the instance's settings, each field as it was typed.
type EditArgs struct {
	Fields []string `arg:"" name:"field=value" help:"Fields to set on this instance."`
}
