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
	slsa1 "github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/v1"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client"
	resourcestypes "github.com/moby/buildkit/executor/resources/types"
	provenancetypes "github.com/moby/buildkit/solver/llbsolver/provenance/types"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"

	"unikraft.com/x/build/progress"
)

// annotationPredicateType tags an in-toto attestation with its predicate type.
const annotationPredicateType = "in-toto.io/predicate-type"

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
		// The history keeps the provenance of each result as an attestation,
		// tagged with its predicate type. Only SLSA v1 decodes as below.
		if d.Annotations[annotationPredicateType] != slsa1.PredicateSLSAProvenance {
			continue
		}
		dt, err := content.ReadBlob(ctx, store, ocispecs.Descriptor{MediaType: d.MediaType, Digest: digest.Digest(d.Digest), Size: d.Size})
		if err != nil {
			return nil, err
		}
		u, err := usageOf(dt)
		if err != nil {
			return nil, fmt.Errorf("could not decode the provenance of %s: %w", ref, err)
		}
		maps.Copy(usage, u)
	}

	return usage, nil
}

// usageOf keys the usage of the steps in the SLSA v1 provenance dt by their
// vertex digest.
func usageOf(dt []byte) (progress.Usage, error) {
	var pred provenancetypes.ProvenancePredicateSLSA1
	if err := json.Unmarshal(dt, &pred); err != nil {
		return nil, err
	}
	bc := pred.BuildDefinition.InternalParameters.BuildConfig
	if bc == nil {
		return nil, nil
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

	return usage, nil
}
