// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package buildkit

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/distribution/reference"
	"unikraft.com/cloud/sdk/platform"
)

// DefaultImage is the image new builders run, callers should pin theirs with
// WithImage.
const DefaultImage = "official/utils/buildkit:latest"

const (
	port     = 1234
	cacheDir = "/var/lib/buildkit"

	readyTimeout = 2 * time.Minute
)

// The errors of Platform.
var (
	// ErrQuota is returned when the account's quota refuses the builder.
	ErrQuota = errors.New("builder quota exceeded")

	// ErrConflict is returned by Check and Acquire when the builder exists with other sizes.
	ErrConflict = errors.New("builder conflict")
)

// Key identifies a builder and sizes a new one, zero taking the Platform's
// option, but for AutokillMs, the stopped time that deletes it, where zero never does.
type Key struct {
	// Name is the builder's name, "default" when empty.
	Name string

	Image      string
	VCPUs      int32
	MemoryMB   int64
	CacheMB    uint64
	AutokillMs uint64
}

// Platform runs builders as instances in the caller's Unikraft Cloud account.
type Platform struct {
	c        platform.Client
	image    string
	vcpus    int32
	memoryMB int64
	cacheMB  uint64
	cooldown time.Duration
	insecure bool
}

// Option configures a Platform.
type Option func(*Platform)

// WithImage sets the image that new builders run. Existing builders keep
// theirs.
func WithImage(ref string) Option {
	return func(p *Platform) { p.image = ref }
}

// WithVCPUs sets the vCPUs of new builders, 4 by default.
func WithVCPUs(n int32) Option {
	return func(p *Platform) { p.vcpus = n }
}

// WithMemory sets the memory of new builders in MiB, 8192 by default.
func WithMemory(mb int64) Option {
	return func(p *Platform) { p.memoryMB = mb }
}

// WithCacheSize sets the size of new builders' cache volume in MiB, 20480 by
// default. The build cache is kept to half of it, see gcKeepStorage.
func WithCacheSize(mb uint64) Option {
	return func(p *Platform) { p.cacheMB = mb }
}

// WithCooldown sets how long new builders stay idle before they scale to zero,
// 500 milliseconds by default.
func WithCooldown(d time.Duration) Option {
	return func(p *Platform) { p.cooldown = d }
}

func WithInsecure(insecure bool) Option {
	return func(p *Platform) { p.insecure = insecure }
}

// NewPlatform returns a Platform that manages builders through c, which
// carries the caller's token and metro.
func NewPlatform(c platform.Client, opts ...Option) *Platform {
	p := &Platform{
		c:        c,
		image:    DefaultImage,
		vcpus:    4,
		memoryMB: 8192,
		cacheMB:  20480,
		cooldown: 500 * time.Millisecond,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Create creates the builder for key, which starts and then scales to zero,
// without waiting for it or connecting to it.
func (p *Platform) Create(ctx context.Context, key Key) error {
	return p.create(ctx, "buildkit-"+cmp.Or(key.Name, "default"), key)
}

// Check returns ErrConflict if the builder for key exists, but not at key's sizes.
func (p *Platform) Check(ctx context.Context, key Key) error {
	if key.Image == "" && key.VCPUs == 0 && key.MemoryMB == 0 && key.CacheMB == 0 {
		return nil
	}
	name := "buildkit-" + cmp.Or(key.Name, "default")
	inst, err := p.get(ctx, name)
	if err != nil {
		return err
	}
	return p.conflict(ctx, name, inst, key)
}

// conflict returns ErrConflict if inst, if any, or its cache volume is not at key's sizes.
func (p *Platform) conflict(ctx context.Context, name string, inst *platform.Instance, key Key) error {
	if inst == nil {
		return nil
	}
	switch {
	case key.VCPUs != 0 && int64(inst.Vcpus) != int64(key.VCPUs):
		return fmt.Errorf("%w: builder %s has %d vcpus, not %d", ErrConflict, name, inst.Vcpus, key.VCPUs)
	case key.MemoryMB != 0 && inst.MemoryMb != uint64(key.MemoryMB):
		return fmt.Errorf("%w: builder %s has %d MiB of memory, not %d", ErrConflict, name, inst.MemoryMb, key.MemoryMB)
	case key.Image != "" && !sameImage(inst.Image, key.Image):
		return fmt.Errorf("%w: builder %s runs %s, not %s", ErrConflict, name, inst.Image, key.Image)
	}
	i := slices.IndexFunc(inst.Volumes, func(v platform.InstanceVolume) bool { return v.At == cacheDir })
	if key.CacheMB == 0 || i < 0 {
		return nil
	}
	resp, err := p.c.GetVolumes(ctx, []platform.NameOrUUID{{Uuid: &inst.Volumes[i].Uuid}}, platform.GetVolumesOpts{})
	if err != nil {
		return fmt.Errorf("getting the cache volume of builder %s: %w", name, err)
	}
	if resp.Data == nil || len(resp.Data.Volumes) == 0 {
		return nil
	}
	if size := resp.Data.Volumes[0].SizeMb; size != key.CacheMB {
		return fmt.Errorf("%w: builder %s has a cache of %d MiB, not %d", ErrConflict, name, size, key.CacheMB)
	}
	return nil
}

// sameImage reports whether have and want name the same image, comparing the
// registry, tag and digest when both name one.
func sameImage(have, want string) bool {
	h, err := reference.ParseNormalizedNamed(have)
	w, werr := reference.ParseNormalizedNamed(want)
	if err != nil || werr != nil {
		return have == want
	}
	hr, hn := imageParts(h)
	wr, wn := imageParts(w)
	if hr != wr {
		return false
	}
	for i := range hn {
		if hn[i] != "" && wn[i] != "" && hn[i] != wn[i] {
			return false
		}
	}
	return true
}

// imageParts returns the repository of ref, and the registry, tag and digest
// it names, empty for those it leaves out.
func imageParts(ref reference.Named) (repo string, named [3]string) {
	domain := reference.Domain(ref)
	if domain == "docker.io" {
		domain = "" // what a bare name implies
	}
	tag, digest := "", ""
	if t, ok := ref.(reference.Tagged); ok {
		tag = t.Tag()
	}
	if d, ok := ref.(reference.Digested); ok {
		digest = d.Digest().String()
	}
	return reference.Path(ref), [3]string{domain, tag, digest}
}

// instance returns the named builder instance, creating it if there is none
// and starting it if it was stopped.
func (p *Platform) instance(ctx context.Context, name string, key Key) (*platform.Instance, error) {
	inst, err := p.get(ctx, name)
	if err != nil {
		return nil, err
	}
	if inst == nil {
		cerr := p.create(ctx, name, key)
		// Look again either way, a concurrent Acquire may have created it.
		if inst, err = p.get(ctx, name); inst == nil {
			return nil, cmp.Or(cerr, err, fmt.Errorf("builder %s not found after creating it", name))
		}
	}
	if err := p.conflict(ctx, name, inst, key); err != nil {
		return nil, err
	}

	// Unlike standby, shutting down refuses connections and stopped ignores them.
	for inst.State == platform.InstanceStateDraining || inst.State == platform.InstanceStateStopping {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for builder %s to stop: %w", name, context.Cause(ctx))
		case <-time.After(time.Second):
		}
		if inst, err = p.get(ctx, name); err != nil {
			return nil, err
		}
		if inst == nil {
			return nil, fmt.Errorf("builder %s was deleted while stopping", name)
		}
	}
	// The edge also wakes a builder in standby, but only a start tells why the
	// platform refuses it.
	if inst.State == platform.InstanceStateStopped || inst.State == platform.InstanceStateStandby {
		_, err := p.c.StartInstanceByUUID(ctx, inst.Uuid, platform.StartInstanceByUUIDRequestBody{})
		// A concurrent Acquire may have started it.
		if err != nil && !platform.ErrorContainsOnly(err, platform.APIHTTPErrorFailedWrongVMState) {
			return nil, fmt.Errorf("starting builder %s: %w", name, quota(err))
		}
	}
	return inst, nil
}

// get returns the named builder instance, or nil if there is none.
func (p *Platform) get(ctx context.Context, name string) (*platform.Instance, error) {
	resp, err := p.c.GetInstances(ctx, []platform.NameOrUUID{{Name: &name}}, platform.GetInstancesOpts{})
	if platform.ErrorContainsOnly(err, platform.APIHTTPErrorNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting builder %s: %w", name, err)
	}
	if resp.Data == nil || len(resp.Data.Instances) == 0 {
		return nil, nil
	}
	return &resp.Data.Instances[0], nil
}

// create creates the named builder instance and its cache volume, with
// newly generated mTLS certificates and keys in its env.
func (p *Platform) create(ctx context.Context, name string, key Key) error {
	cacheMB := cmp.Or(key.CacheMB, p.cacheMB)
	env, err := generateCerts()
	if err != nil {
		return fmt.Errorf("generating certificates: %w", err)
	}
	env[EnvOCIWorkerGCKeepStorage] = gcKeepStorage(cacheMB)
	_, err = p.c.CreateInstance(ctx, platform.CreateInstanceRequest{
		Name:      &name,
		Image:     platform.ImageReference(cmp.Or(key.Image, p.image)),
		Vcpus:     new(cmp.Or(key.VCPUs, p.vcpus)),
		MemoryMb:  new(cmp.Or(key.MemoryMB, p.memoryMB)),
		Autostart: new(true),
		Env:       env,
		Volumes:   []platform.CreateInstanceRequestVolume{{SizeMb: new(cacheMB), Filesystem: new("ext4"), At: cacheDir}},
		ServiceGroup: &platform.CreateInstanceRequestServiceGroup{
			Services: []platform.Service{{
				Port:            port,
				DestinationPort: new(uint32(port)),
				Handlers:        []platform.ConnectionHandler{platform.ConnectionHandlerTls},
			}},
		},
		ScaleToZero: &platform.CreateInstanceScaleToZero{
			Policy:         new(platform.InstanceScaleToZeroPolicyOn),
			Stateful:       new(true),
			CooldownTimeMs: new(int32(p.cooldown.Milliseconds())),
		},
		Autokill: &platform.CreateInstanceRequestAutokill{TimeMs: new(key.AutokillMs)},
	})
	if err != nil {
		return fmt.Errorf("creating builder %s: %w", name, quota(err))
	}
	return nil
}

// quota adds ErrQuota to err if the platform refused the request because of
// the caller's quota.
func quota(err error) error {
	if platform.ErrorContains(err, platform.APIHTTPErrorQuota) {
		return fmt.Errorf("%w: %w", ErrQuota, err)
	}
	return err
}

// gcKeepStorage returns the garbage collection limits for a cache volume of
// the given size in MiB, in the "reserved,free,max" MB form of
// EnvOCIWorkerGCKeepStorage.
func gcKeepStorage(cacheMiB uint64) string {
	mb := cacheMiB * (1 << 20) / 1_000_000
	return fmt.Sprintf("%d,%d,%d", mb/10, mb/2, mb/2)
}
