// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package fingerprint provides a common structure used to identify machines.
package fingerprint

type Fingerprint struct {
	// MachineId is a unique identifier for the machine, typically derived
	// from hardware or system properties.
	MachineId string `json:"machine_id" oid:"1,critical"`

	// The hostname is the name of the machine.  This is mandatory as it
	// consitutes a unique identifier for the machine.
	Hostname string `json:"hostname" oid:"2,critical"`

	// The CPU details of the machine.
	CpuCores     *int32   `json:"cpus,omitempty" oid:"23,omitempty"`
	CpusThreads  *int32   `json:"cpu_threads,omitempty" oid:"24,omitempty"`
	CpuVendorId  *string  `json:"cpu_vendor_id" oid:"3,omitempty"`
	CpuFamily    *string  `json:"cpu_family" oid:"4,omitempty"`
	CpuModel     *string  `json:"cpu_model" oid:"5,omitempty"`
	CpuModelName *string  `json:"cpu_model_name" oid:"6,omitempty"`
	CpuMhz       *float64 `json:"cpu_mhz" oid:"7,omitempty"`
	CpuCacheSize *int32   `json:"cpu_cache_size" oid:"8,omitempty"`
	CpuFlags     []string `json:"cpu_flags" oid:"9,omitempty"`
	CpuMicrocode *string  `json:"cpu_microcode" oid:"10,omitempty"`

	// The total amount of memory (RAM) of the machine in bytes.
	MemTotal *int64 `json:"mem_total,omitempty" oid:"25,omitempty"`

	// The operating system of the machine.
	Os string `json:"os" oid:"11,critical"`

	// The version of the operating system of the machine, if available.
	//
	// For Android, it's like "10", "11", "12", etc.  For iOS and macOS it's like
	// "15.6.1" or "12.4.0".  For Windows it's like "10.0.19044.1889". For FreeBSD
	// it's like "12.3-STABLE".  For Linux, this is simply the kernel version on
	// Linux, like "5.10.0-17-amd64".
	OsVersion *string `json:"os_version,omitempty" oid:"12,omitempty"`

	// A best-effort whether the client is running in a container.
	Container bool `json:"container,omitempty" oid:"13,omitempty"`

	// The OS distribution, if known.  E.g. "debian", "ubuntu", "nixos", ...
	Distro *string `json:"distro,omitempty" oid:"14,omitempty"`

	// The OS distribution version if known.  E.g. "20.04", ...
	DistroVersion *string `json:"distro_version,omitempty" oid:"15,omitempty"`

	// TThe OS distribution codename if known.  E.g. "jammy", "bullseye", ...
	DistroCodename *string `json:"distro_codename,omitempty" oid:"16,omitempty"`

	// The GOARCH value of the binary (e.g., "amd64", "arm64", ...).
	Goarch string `json:"goarch,omitempty" oid:"17,omitempty"`

	// The GOOS value of the binary.
	Goos string `json:"goos,omitempty" oid:"18,omitempty"`

	// The Go version binary was built with (if available).
	GoVersion *string `json:"go_version,omitempty" oid:"19,omitempty"`

	// Lists of available kernel features (e.g., "kvm", "virtio-net").
	// Only applies to Linux.
	KernelFeatures []string `json:"kernel_features,omitempty" oid:"20,omitempty"`

	// The kernel release of the underlying host, if available.
	KernelRelease *string `json:"kernel_release,omitempty" oid:"21,omitempty"`

	// The kernel version of the underlying host, if available.
	KernelVersion *string `json:"kernel_version,omitempty" oid:"22,omitempty"`
}
