// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package processmetrics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"golang.org/x/sys/unix"
)

// TestStartReportsProcessMetrics pins the metric set, because a dashboard
// built on these names cannot tell a rename from a process that stopped
// reporting. Each name maps to the number of attribute sets it must report
// under, so a dropped cpu mode or io direction fails here rather than
// silently halving a graph.
func TestStartReportsProcessMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	require.NoError(t, Start())

	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &collected))

	points := map[string]int{}
	single := map[string]int64{}

	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			switch data := m.Data.(type) {
			case metricdata.Sum[int64]:
				points[m.Name] = len(data.DataPoints)
				if len(data.DataPoints) == 1 {
					single[m.Name] = data.DataPoints[0].Value
				}
			case metricdata.Sum[float64]:
				points[m.Name] = len(data.DataPoints)
			case metricdata.Gauge[float64]:
				points[m.Name] = len(data.DataPoints)
			}
		}
	}

	want := map[string]int{
		"process.cpu.time":                   2,
		"process.memory.usage":               1,
		"process.memory.virtual":             1,
		"process.disk.io":                    2,
		"process.thread.count":               1,
		"process.unix.file_descriptor.count": 1,
		"process.context_switches":           2,
		"process.paging.faults":              2,
		"process.uptime":                     1,
	}

	// An unlimited resource is deliberately not reported, so what the limits
	// should look like depends on the machine running the test.
	want[fdLimitName] = boundedPoints(t, false)
	want[fdLimitMaxName] = boundedPoints(t, true)

	for name, count := range want {
		assert.Equal(t, count, points[name], "%s data points", name)
	}

	// The invariant the package exists for.
	if want[fdLimitName] == 1 {
		assert.Positive(t, single[fdLimitName])
		assert.LessOrEqual(t, single["process.unix.file_descriptor.count"], single[fdLimitName])
	}

	assert.Positive(t, single["process.unix.file_descriptor.count"])
	assert.Positive(t, single["process.thread.count"])
	assert.Positive(t, single["process.memory.usage"])
}

// boundedPoints reports how many data points a descriptor limit instrument
// should carry: one when the limit is bounded, none when it is unlimited.
func boundedPoints(t *testing.T, hard bool) int {
	t.Helper()

	var limit unix.Rlimit
	require.NoError(t, unix.Getrlimit(unix.RLIMIT_NOFILE, &limit))

	value := limit.Cur
	if hard {
		value = limit.Max
	}

	if value == unix.RLIM_INFINITY {
		return 0
	}

	return 1
}
