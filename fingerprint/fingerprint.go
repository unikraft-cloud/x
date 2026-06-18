// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package fingerprint provides a common structure used to identify machines.
package fingerprint

import (
	"bytes"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"github.com/denisbrodbeck/machineid"
	"github.com/gofrs/uuid/v5"
	"github.com/shirou/gopsutil/v3/cpu"
	"golang.org/x/sys/unix"
	"tailscale.com/hostinfo"
	"tailscale.com/util/dnsname"

	"unikraft.com/x/ptr"
)

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

func New() (*Fingerprint, error) {
	host := hostinfo.New()
	container, _ := host.Container.Get()

	if runtime.GOOS == "darwin" {
		var err error
		host.OSVersion, err = getMacOSVersion()
		if err != nil {
			return nil, err
		}
	}

	machineId, err := machineid.ID()
	if err != nil {
		return nil, err
	}

	// Ensure the machine ID is in lowercase if it's a valid UUID.
	if !uuid.FromStringOrNil(machineId).IsNil() {
		machineId = strings.ToLower(machineId)
	}

	cpuInfo, err := cpu.Info()
	if err != nil {
		return nil, err
	}

	kernelRelease, kernelVersion := getKernelReleaseVersion()

	return &Fingerprint{
		MachineId:      machineId,
		Hostname:       dnsname.TrimCommonSuffixes(host.Hostname),
		CpuCores:       ptr.NilIfZero(cpuInfo[0].Cores),
		CpusThreads:    new(int32(len(cpuInfo))),
		CpuVendorId:    ptr.NilIfZero(cpuInfo[0].VendorID),
		CpuFamily:      ptr.NilIfZero(cpuInfo[0].Family),
		CpuModel:       ptr.NilIfZero(cpuInfo[0].Model),
		CpuModelName:   ptr.NilIfZero(cpuInfo[0].ModelName),
		CpuCacheSize:   ptr.NilIfZero(cpuInfo[0].CacheSize),
		CpuMhz:         ptr.NilIfZero(cpuInfo[0].Mhz),
		CpuFlags:       cpuInfo[0].Flags,
		CpuMicrocode:   ptr.NilIfZero(cpuInfo[0].Microcode),
		Os:             host.OS,
		Container:      container,
		Distro:         ptr.NilIfZero(host.Distro),
		DistroCodename: ptr.NilIfZero(host.DistroCodeName),
		DistroVersion:  ptr.NilIfZero(host.DistroVersion),
		Goarch:         runtime.GOARCH,
		Goos:           runtime.GOOS,
		GoVersion:      ptr.NilIfZero(runtime.Version()),
		OsVersion:      ptr.NilIfZero(host.OSVersion),
		KernelFeatures: detectKernelFeatures(),
		KernelRelease:  ptr.NilIfZero(kernelRelease),
		KernelVersion:  ptr.NilIfZero(kernelVersion),
	}, nil
}

// getMacOSVersion retrieves the macOS version using the `sw_vers` command.
func getMacOSVersion() (string, error) {
	cmd := exec.Command("sw_vers", "-productVersion")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(output))
	return version, nil
}

func cstrToStr(b []byte) string {
	return string(b[:bytes.IndexByte(b, 0)])
}

// getKernelVersion retrieves the kernel version details from the Uname system.
func getKernelReleaseVersion() (string, string) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return "", ""
	}

	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return "", ""
	}

	return cstrToStr(u.Release[:]), cstrToStr(u.Version[:])
}

// kernelFeatureCache caches kernel feature detection data to avoid repeated
// file reads.
type kernelFeatureCache struct {
	once             sync.Once
	procFilesystems  []byte
	procModules      []byte
	procCpuinfo      []byte
	filesystemsError error
	modulesError     error
	cpuinfoError     error
}

var kfCache kernelFeatureCache

// initCache initializes the kernel feature cache by reading necessary files
// once.
func (c *kernelFeatureCache) init() {
	c.once.Do(func() {
		c.procFilesystems, c.filesystemsError = os.ReadFile("/proc/filesystems")
		c.procModules, c.modulesError = os.ReadFile("/proc/modules")
		c.procCpuinfo, c.cpuinfoError = os.ReadFile("/proc/cpuinfo")
	})
}

// detectKernelFeatures detects available kernel features efficiently.  It reads
// system files only once and performs all checks against cached data.
func detectKernelFeatures() []string {
	// Skip detection on non-Linux systems
	if runtime.GOOS != "linux" {
		return nil
	}

	// Initialize cache (only happens once)
	kfCache.init()

	// Preallocate with estimated capacity to avoid reallocation
	features := make([]string, 0, 10)

	// Virtualization support
	if hasKvmSupport() {
		features = append(features, "kvm")
	}

	if hasVirtioSupport() {
		features = append(features, "virtio")
	}

	// Container and namespace features
	if hasCgroupsV1() {
		features = append(features, "cgroups-v1")
	}

	if hasCgroupsV2() {
		features = append(features, "cgroups-v2")
	}

	if hasNamespaceSupport() {
		features = append(features, "namespaces")
	}

	// Filesystem features
	if hasOverlayFS() {
		features = append(features, "overlayfs")
	}

	if hasBtrfs() {
		features = append(features, "btrfs")
	}

	if hasXfs() {
		features = append(features, "xfs")
	}

	// CPU features (useful for virtualization)
	if hasVmxSupport() {
		features = append(features, "vmx") // Intel VT-x
	}

	if hasSvmSupport() {
		features = append(features, "svm") // AMD-V
	}

	return features
}

// hasKvmSupport checks if Linux KVM virtualization is available.
func hasKvmSupport() bool {
	_, err := os.Stat("/dev/kvm")
	return err == nil
}

// hasVirtioSupport checks if virtio support is available.
func hasVirtioSupport() bool {
	// Check for virtio devices
	if entries, err := os.ReadDir("/sys/bus/virtio/devices"); err == nil && len(entries) > 0 {
		return true
	}

	// Check loaded modules (using cached data)
	if kfCache.modulesError == nil && bytes.Contains(kfCache.procModules, []byte("virtio")) {
		return true
	}

	return false
}

// hasCgroupsV1 checks if cgroups v1 is available.
func hasCgroupsV1() bool {
	if kfCache.filesystemsError != nil {
		return false
	}

	// Check for "cgroup" but not "cgroup2" in /proc/filesystems
	for line := range bytes.SplitSeq(kfCache.procFilesystems, []byte("\n")) {
		if bytes.Contains(line, []byte("cgroup")) && !bytes.Contains(line, []byte("cgroup2")) {
			return true
		}
	}

	return false
}

// hasCgroupsV2 checks if cgroups v2 is available.
func hasCgroupsV2() bool {
	if kfCache.filesystemsError != nil {
		return false
	}

	return bytes.Contains(kfCache.procFilesystems, []byte("cgroup2"))
}

// hasNamespaceSupport checks if Linux namespaces are supported.
func hasNamespaceSupport() bool {
	// Check if /proc/self/ns exists (available since Linux 3.8)
	info, err := os.Stat("/proc/self/ns")
	return err == nil && info.IsDir()
}

// hasOverlayFS checks if overlay filesystem is supported.
func hasOverlayFS() bool {
	if kfCache.filesystemsError != nil {
		return false
	}

	return bytes.Contains(kfCache.procFilesystems, []byte("overlay"))
}

// hasBtrfs checks if btrfs filesystem is supported.
func hasBtrfs() bool {
	if kfCache.filesystemsError != nil {
		return false
	}

	return bytes.Contains(kfCache.procFilesystems, []byte("btrfs"))
}

// hasXfs checks if XFS filesystem is supported.
func hasXfs() bool {
	if kfCache.filesystemsError != nil {
		return false
	}

	return bytes.Contains(kfCache.procFilesystems, []byte("xfs"))
}

// hasVmxSupport checks if Intel VT-x (VMX) is available.
func hasVmxSupport() bool {
	if kfCache.cpuinfoError != nil {
		return false
	}

	return bytes.Contains(kfCache.procCpuinfo, []byte("vmx"))
}

// hasSvmSupport checks if AMD-V (SVM) is available.
func hasSvmSupport() bool {
	if kfCache.cpuinfoError != nil {
		return false
	}

	return bytes.Contains(kfCache.procCpuinfo, []byte("svm"))
}
