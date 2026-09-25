// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !js

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
	"github.com/shirou/gopsutil/v3/mem"
	"golang.org/x/sys/unix"
	"tailscale.com/hostinfo"
	"tailscale.com/util/dnsname"

	"unikraft.com/x/ptr"
)

func New(opts ...Option) (*Fingerprint, error) {
	o := options{hardware: true}
	for _, opt := range opts {
		opt(&o)
	}

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

	if !uuid.FromStringOrNil(machineId).IsNil() {
		machineId = strings.ToLower(machineId)
	}

	kernelRelease, kernelVersion := getKernelReleaseVersion()

	f := &Fingerprint{
		MachineId:      machineId,
		Hostname:       dnsname.TrimCommonSuffixes(host.Hostname),
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
	}
	if !o.hardware {
		return f, nil
	}

	cpuInfo, err := cpu.Info()
	if err != nil {
		return nil, err
	}

	cpuCores, err := cpu.Counts(false)
	if err != nil {
		return nil, err
	}

	memInfo, err := mem.VirtualMemory()
	if err != nil {
		return nil, err
	}

	f.CpuCores = ptr.NilIfZero(int32(cpuCores))
	f.CpusThreads = new(int32(len(cpuInfo)))
	f.CpuVendorId = ptr.NilIfZero(cpuInfo[0].VendorID)
	f.CpuFamily = ptr.NilIfZero(cpuInfo[0].Family)
	f.CpuModel = ptr.NilIfZero(cpuInfo[0].Model)
	f.CpuModelName = ptr.NilIfZero(cpuInfo[0].ModelName)
	f.CpuCacheSize = ptr.NilIfZero(cpuInfo[0].CacheSize)
	f.CpuMhz = ptr.NilIfZero(cpuInfo[0].Mhz)
	f.CpuFlags = cpuInfo[0].Flags
	f.CpuMicrocode = ptr.NilIfZero(cpuInfo[0].Microcode)
	f.MemTotal = ptr.NilIfZero(int64(memInfo.Total))
	return f, nil
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
	if runtime.GOOS != "linux" {
		return nil
	}

	kfCache.init()

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
