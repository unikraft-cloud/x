// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/content/proxy"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client"
	resourcestypes "github.com/moby/buildkit/executor/resources/types"
	provenancetypes "github.com/moby/buildkit/solver/llbsolver/provenance/types"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"

	"unikraft.com/x/build/progress"
)

// stepUsage reads what the steps of the solve ref used from buildkit's history.
func stepUsage(ctx context.Context, c *client.Client, ref string) (progress.Usage, error) {
	history, err := c.ControlClient().ListenBuildHistory(ctx, &controlapi.BuildHistoryRequest{Ref: ref, EarlyExit: true})
	if err != nil {
		return nil, err
	}

	var attestations []*controlapi.Descriptor
	for {
		ev, err := history.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		rec := ev.GetRecord()
		if rec == nil || rec.Ref != ref {
			continue
		}
		for _, res := range rec.Results {
			attestations = append(attestations, res.GetAttestations()...)
		}
		attestations = append(attestations, rec.Result.GetAttestations()...)
	}

	store := proxy.NewContentStore(c.ContentClient())
	usage := progress.Usage{}
	for _, d := range attestations {
		if d.Annotations["in-toto.io/predicate-type"] != "https://slsa.dev/provenance/v1" {
			continue
		}
		dt, err := content.ReadBlob(ctx, store, ocispecs.Descriptor{MediaType: d.MediaType, Digest: digest.Digest(d.Digest), Size: d.Size})
		if err != nil {
			return nil, err
		}
		var pred provenancetypes.ProvenancePredicateSLSA1
		if err := json.Unmarshal(dt, &pred); err != nil {
			return nil, fmt.Errorf("could not decode the provenance of %s: %w", ref, err)
		}
		maps.Copy(usage, usageOf(pred.BuildDefinition.InternalParameters.BuildConfig))
	}

	return usage, nil
}

// usageOf keys the usage of the steps of bc by their vertex digest.
func usageOf(bc *provenancetypes.BuildConfig) progress.Usage {
	if bc == nil {
		return nil
	}

	steps := make(map[string]*resourcestypes.Samples, len(bc.Definition))
	for _, s := range bc.Definition {
		if s.ResourceUsage != nil {
			steps[s.ID] = s.ResourceUsage
		}
	}

	usage := make(progress.Usage, len(steps))
	for d, id := range bc.DigestMapping {
		if s := steps[id]; s != nil {
			usage[d] = s
		}
	}

	return usage
}
