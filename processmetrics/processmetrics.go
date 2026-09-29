// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package processmetrics reports the OS process metrics of the semantic
// conventions, read from procfs, plus the descriptor ceilings the conventions
// have no metric for.
//
// See https://opentelemetry.io/docs/specs/semconv/system/process-metrics/ for
// the set this implements. Not all of it is reported:
//
//   - process.network.io reads /proc/self/net/dev, which describes the network
//     namespace rather than the process, and would be wrong wherever one is
//     shared.
//   - process.cpu.utilization and process.memory.utilization are opt-in
//     ratios, which a backend derives from the counters better than we can
//     sample them.
//   - process.disk.operations and process.signals_pending have no processconv
//     constructor, and process.windows.handle.count is not ours to report.
//
// No instrumentation library collects these: contrib offers host and runtime
// only, and neither reads a process's own descriptors. The instruments come
// from semconv's processconv so the names, units and descriptions are not ours
// to get wrong; only the readings are.
package processmetrics

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/prometheus/procfs"
	"github.com/tklauser/go-sysconf"
	"golang.org/x/sys/unix"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/semconv/v1.43.0/processconv"
)

// scopeName identifies the instruments this package registers.
const scopeName = "unikraft.com/x/processmetrics"

// The conventions define metrics for what a process uses but nothing for what
// it is allowed to use, so these three extend them: .limit for the rlimit
// bounding a convention metric, and .limit.max for the ceiling that limit may
// be raised to. The suffix follows the conventions' own use of it, as in
// go.memory.limit.
const (
	fdLimitName    = "process.unix.file_descriptor.limit"
	fdLimitMaxName = "process.unix.file_descriptor.limit.max"
)

// Start registers the process metrics with the global meter provider, and must
// therefore run after telemetry.Init has installed one. Go runtime metrics
// are a separate concern and come from contrib's runtime instrumentation.
func Start() error {
	self, err := procfs.Self()
	if err != nil {
		return fmt.Errorf("reading procfs: %w", err)
	}

	// The CPU times in /proc/self/stat are in USER_HZ, which is the userspace
	// ABI rate rather than the kernel's own CONFIG_HZ, so it has to be asked
	// for rather than assumed.
	ticks, err := sysconf.Sysconf(sysconf.SC_CLK_TCK)
	if err != nil {
		return fmt.Errorf("reading clock tick rate: %w", err)
	}

	meter := otel.Meter(scopeName)

	cpuTime, err := processconv.NewCPUTime(meter)
	if err != nil {
		return fmt.Errorf("registering cpu time: %w", err)
	}

	memoryUsage, err := processconv.NewMemoryUsageObservable(meter)
	if err != nil {
		return fmt.Errorf("registering memory usage: %w", err)
	}

	memoryVirtual, err := processconv.NewMemoryVirtualObservable(meter)
	if err != nil {
		return fmt.Errorf("registering virtual memory: %w", err)
	}

	diskIO, err := processconv.NewDiskIOObservable(meter)
	if err != nil {
		return fmt.Errorf("registering disk io: %w", err)
	}

	threads, err := processconv.NewThreadCountObservable(meter)
	if err != nil {
		return fmt.Errorf("registering thread count: %w", err)
	}

	descriptors, err := processconv.NewUnixFileDescriptorCountObservable(meter)
	if err != nil {
		return fmt.Errorf("registering descriptor count: %w", err)
	}

	switches, err := processconv.NewContextSwitchesObservable(meter)
	if err != nil {
		return fmt.Errorf("registering context switches: %w", err)
	}

	faults, err := processconv.NewPagingFaultsObservable(meter)
	if err != nil {
		return fmt.Errorf("registering paging faults: %w", err)
	}

	uptime, err := processconv.NewUptimeObservable(meter)
	if err != nil {
		return fmt.Errorf("registering uptime: %w", err)
	}

	descriptorLimit, err := meter.Int64ObservableUpDownCounter(
		fdLimitName,
		metric.WithDescription("Maximum number of unix file descriptors the process may open."),
		metric.WithUnit("{file_descriptor}"),
	)
	if err != nil {
		return fmt.Errorf("registering descriptor limit: %w", err)
	}

	descriptorLimitMax, err := meter.Int64ObservableUpDownCounter(
		fdLimitMaxName,
		metric.WithDescription("Ceiling the process's unix file descriptor limit may be raised to."),
		metric.WithUnit("{file_descriptor}"),
	)
	if err != nil {
		return fmt.Errorf("registering descriptor limit ceiling: %w", err)
	}

	// The conventions describe more than this; the package comment says which
	// of them are left out, and why.
	collector := &collector{
		self:  self,
		ticks: float64(ticks),

		cpuTime:            cpuTime,
		memoryUsage:        memoryUsage,
		memoryVirtual:      memoryVirtual,
		diskIO:             diskIO,
		threads:            threads,
		descriptors:        descriptors,
		descriptorLimit:    descriptorLimit,
		descriptorLimitMax: descriptorLimitMax,
		switches:           switches,
		faults:             faults,
		uptime:             uptime,
	}
	if _, err := meter.RegisterCallback(collector.observe, collector.instruments()...); err != nil {
		return fmt.Errorf("registering process metrics callback: %w", err)
	}

	return nil
}

type collector struct {
	self  procfs.Proc
	ticks float64

	cpuTime            processconv.CPUTime
	memoryUsage        processconv.MemoryUsageObservable
	memoryVirtual      processconv.MemoryVirtualObservable
	diskIO             processconv.DiskIOObservable
	threads            processconv.ThreadCountObservable
	descriptors        processconv.UnixFileDescriptorCountObservable
	descriptorLimit    metric.Int64ObservableUpDownCounter
	descriptorLimitMax metric.Int64ObservableUpDownCounter
	switches           processconv.ContextSwitchesObservable
	faults             processconv.PagingFaultsObservable
	uptime             processconv.UptimeObservable
}

// instruments returns every instrument observe reports, so that the
// registration cannot fall out of step with the callback.
func (c *collector) instruments() []metric.Observable {
	return []metric.Observable{
		c.cpuTime.Inst(),
		c.memoryUsage.Inst(),
		c.memoryVirtual.Inst(),
		c.diskIO.Inst(),
		c.threads.Inst(),
		c.descriptors.Inst(),
		c.descriptorLimit,
		c.descriptorLimitMax,
		c.switches.Inst(),
		c.faults.Inst(),
		c.uptime.Inst(),
	}
}

// observe reads the process's procfs entries and reports every instrument. A
// file that cannot be read costs only the metrics it feeds: the errors are
// joined and returned, and the SDK keeps whatever was observed before them.
func (c *collector) observe(_ context.Context, o metric.Observer) error {
	var errs []error

	var descriptorLimit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &descriptorLimit); err != nil {
		errs = append(errs, fmt.Errorf("reading descriptor limit: %w", err))
	} else {
		// RLIM_INFINITY is a sentinel rather than a quantity, and not the same
		// one on every platform, so an unlimited resource goes unreported.
		if descriptorLimit.Cur != unix.RLIM_INFINITY {
			o.ObserveInt64(c.descriptorLimit, int64(descriptorLimit.Cur))
		}
		if descriptorLimit.Max != unix.RLIM_INFINITY {
			o.ObserveInt64(c.descriptorLimitMax, int64(descriptorLimit.Max))
		}
	}

	// The directory handle held while counting is itself a descriptor, so the
	// count runs one high; that is not worth correcting for against a ceiling
	// in the thousands.
	if count, err := c.self.FileDescriptorsLen(); err != nil {
		errs = append(errs, fmt.Errorf("counting descriptors: %w", err))
	} else {
		o.ObserveInt64(c.descriptors.Inst(), int64(count))
	}

	if stat, err := c.self.Stat(); err != nil {
		errs = append(errs, fmt.Errorf("reading process stat: %w", err))
	} else {
		o.ObserveFloat64(c.cpuTime.Inst(), float64(stat.UTime)/c.ticks,
			metric.WithAttributes(c.cpuTime.AttrCPUMode(processconv.CPUModeUser)))
		o.ObserveFloat64(c.cpuTime.Inst(), float64(stat.STime)/c.ticks,
			metric.WithAttributes(c.cpuTime.AttrCPUMode(processconv.CPUModeSystem)))

		o.ObserveInt64(c.memoryUsage.Inst(), int64(stat.ResidentMemory()))
		o.ObserveInt64(c.memoryVirtual.Inst(), int64(stat.VirtualMemory()))
		o.ObserveInt64(c.threads.Inst(), int64(stat.NumThreads))

		o.ObserveInt64(c.faults.Inst(), int64(stat.MinFlt),
			metric.WithAttributes(c.faults.AttrSystemPagingFaultType(processconv.SystemPagingFaultTypeMinor)))
		o.ObserveInt64(c.faults.Inst(), int64(stat.MajFlt),
			metric.WithAttributes(c.faults.AttrSystemPagingFaultType(processconv.SystemPagingFaultTypeMajor)))

		// StartTime is the process's own, so uptime covers the initialisation
		// that ran before this package was registered.
		if start, err := stat.StartTime(); err != nil {
			errs = append(errs, fmt.Errorf("reading process start time: %w", err))
		} else {
			started := time.Unix(0, int64(start*float64(time.Second)))
			o.ObserveFloat64(c.uptime.Inst(), time.Since(started).Seconds())
		}
	}

	if status, err := c.self.NewStatus(); err != nil {
		errs = append(errs, fmt.Errorf("reading process status: %w", err))
	} else {
		o.ObserveInt64(c.switches.Inst(), int64(status.VoluntaryCtxtSwitches),
			metric.WithAttributes(c.switches.AttrContextSwitchType(processconv.ContextSwitchTypeVoluntary)))
		o.ObserveInt64(c.switches.Inst(), int64(status.NonVoluntaryCtxtSwitches),
			metric.WithAttributes(c.switches.AttrContextSwitchType(processconv.ContextSwitchTypeInvoluntary)))
	}

	if io, err := c.self.IO(); err != nil {
		errs = append(errs, fmt.Errorf("reading process io: %w", err))
	} else {
		o.ObserveInt64(c.diskIO.Inst(), int64(io.ReadBytes),
			metric.WithAttributes(c.diskIO.AttrDiskIODirection(processconv.DiskIODirectionRead)))
		o.ObserveInt64(c.diskIO.Inst(), int64(io.WriteBytes),
			metric.WithAttributes(c.diskIO.AttrDiskIODirection(processconv.DiskIODirectionWrite)))
	}

	return errors.Join(errs...)
}
