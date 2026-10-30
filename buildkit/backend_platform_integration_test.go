// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build integration

package buildkit

import (
	"context"
	"crypto/rand"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unikraft.com/cloud/sdk/pkg/httpclient"
	"unikraft.com/cloud/sdk/platform"
	"unikraft.com/x/integration"
)

// TestPlatformAcquireIntegration creates a builder in the metro of UKC_METRO
// and deletes it afterwards.
func TestPlatformAcquireIntegration(t *testing.T) {
	token := integration.Token(t)
	var opts []httpclient.Option
	if integration.Insecure() {
		opts = append(opts, httpclient.WithInsecure())
	}
	pc := platform.NewClientFromEnv(
		platform.WithToken(token),
		platform.WithHTTPClient(httpclient.NewHTTPClient(opts...)),
	)
	p := NewPlatform(pc,
		WithVCPUs(1),
		WithMemory(1024),
		WithCacheSize(2048),
		WithInsecure(integration.Insecure()),
	)
	key := Key{Name: "test-" + strings.ToLower(rand.Text()[:8])}
	name := "buildkit-" + key.Name
	t.Cleanup(func() { deleteBuilder(t, p, name) })

	// Two first builds race to create the builder.
	var wg sync.WaitGroup
	daemons := make([]*Daemon, 2)
	errs := make([]error, 2)
	for i := range daemons {
		wg.Go(func() { daemons[i], errs[i] = p.Acquire(t.Context(), key) })
	}
	wg.Wait()
	for i, d := range daemons {
		require.NoError(t, errs[i])
		assert.Equal(t, daemons[0].Addr, d.Addr)
		assert.Equal(t, daemons[0].TLS, d.TLS)
	}
	solve(t, daemons[0])
	for _, d := range daemons {
		require.NoError(t, d.Release())
	}

	// A stopped builder does not wake on connect.
	_, err := pc.StopInstances(t.Context(), []platform.StopInstancesRequestItem{{Name: &name}})
	require.NoError(t, err)
	_, err = pc.WaitInstances(t.Context(), []platform.WaitInstancesRequestItem{{Name: &name, State: new(platform.InstanceStateStopped)}}, platform.WaitInstancesOpts{})
	require.NoError(t, err)
	d, err := p.Acquire(t.Context(), key)
	require.NoError(t, err)
	solve(t, d)
	require.NoError(t, d.Release())

	// Nor does one that is still shutting down when Acquire finds it.
	_, err = pc.StopInstances(t.Context(), []platform.StopInstancesRequestItem{{Name: &name, DrainTimeoutMs: new(uint64(10_000))}})
	require.NoError(t, err)
	d, err = p.Acquire(t.Context(), key)
	require.NoError(t, err)
	solve(t, d)
	require.NoError(t, d.Release())
}

func solve(t *testing.T, d *Daemon) {
	t.Helper()
	def, err := llb.Image(integration.Image, llb.LinuxAmd64).Run(llb.Shlex("true")).Root().Marshal(t.Context(), llb.LinuxAmd64)
	require.NoError(t, err)
	_, err = d.Client.Solve(t.Context(), def, client.SolveOpt{}, nil)
	require.NoError(t, err)
}

func deleteBuilder(t *testing.T, p *Platform, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// The cache volume has no name, so find it before the instance is gone.
	inst, err := p.get(ctx, name)
	if err != nil {
		t.Error(err)
	}
	_, err = p.c.DeleteInstances(ctx, []platform.DeleteInstanceRequestItem{{Name: &name, DontRetain: new(true)}})
	if err != nil && !platform.ErrorContainsOnly(err, platform.APIHTTPErrorNotFound) {
		t.Errorf("deleting builder %s: %v", name, err)
	}
	if inst == nil {
		return
	}
	i := slices.IndexFunc(inst.Volumes, func(v platform.InstanceVolume) bool { return v.At == cacheDir })
	if i < 0 {
		return
	}
	// The volume stays attached until the instance is gone.
	vol := inst.Volumes[i].Uuid
	for {
		_, err := p.c.DeleteVolumes(ctx, []platform.NameOrUUID{{Uuid: &vol}})
		if err == nil || platform.ErrorContainsOnly(err, platform.APIHTTPErrorNotFound) {
			return
		}
		select {
		case <-ctx.Done():
			t.Errorf("deleting volume %s: %v", vol, err)
			return
		case <-time.After(3 * time.Second):
		}
	}
}
