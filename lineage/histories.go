// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package lineage

import (
	"cmp"
	"context"
	"errors"
	"slices"

	"unikraft.com/cloud/sdk/platform"
)

// MaxOperations is the most items the platform takes in one request; it
// refuses a longer list outright ("Too many operations in request"): a
// batch of 254 gets HTTP 413.
const MaxOperations = 253

// Histories is what FetchHistories read.
type Histories struct {
	// Instances are the histories that were read.
	Instances []platform.GetCheckpointHistoryResponseInstanceHistory
	// Missing are the UUIDs of the instances whose history failed, alone
	// or with their batch.
	Missing []string
	// Errs are the failures of the batches that failed, when only some did.
	Errs []error
}

// FetchHistories asks for the checkpoint histories of instances in batches
// the platform takes.  A failed batch only misses its instances; it fails
// as a whole (zero Histories, joined error) when every batch does.
func FetchHistories(ctx context.Context, client platform.Client, instances []platform.Instance) (Histories, error) {
	uuids := make([]string, 0, len(instances))
	for _, instance := range instances {
		uuids = append(uuids, instance.Uuid)
	}
	return batchHistories(uuids, func(refs []platform.NameOrUUID) (*platform.Response[platform.GetCheckpointHistoryResponseData], error) {
		return client.GetInstanceHistory(ctx, refs, platform.GetInstanceHistoryOpts{})
	})
}

// batchHistories asks get for the histories of uuids, at most
// MaxOperations at a time.
func batchHistories(
	uuids []string,
	get func([]platform.NameOrUUID) (*platform.Response[platform.GetCheckpointHistoryResponseData], error),
) (Histories, error) {
	refs := make([]platform.NameOrUUID, len(uuids))
	for i := range uuids {
		refs[i].Uuid = &uuids[i]
	}

	var (
		read    Histories
		batches int
	)
	for start := 0; start < len(uuids); start += MaxOperations {
		end := min(start+MaxOperations, len(uuids))
		batches++
		histories, missing, err := splitHistories(get(refs[start:end:end]))
		if err != nil {
			read.Errs = append(read.Errs, err)
			read.Missing = append(read.Missing, uuids[start:end]...)
			continue
		}
		read.Instances = append(read.Instances, histories...)
		read.Missing = append(read.Missing, missing...)
	}
	if len(read.Errs) > 0 && len(read.Errs) == batches {
		return Histories{}, errors.Join(read.Errs...)
	}
	return read, nil
}

// splitHistories keeps what a partial success read.  The SDK fails the
// whole call when some items fail, but still decodes the rest.
func splitHistories(resp *platform.Response[platform.GetCheckpointHistoryResponseData], err error) (
	histories []platform.GetCheckpointHistoryResponseInstanceHistory,
	missing []string,
	_ error,
) {
	if resp == nil || resp.Data == nil {
		return nil, nil, cmp.Or(err, errors.New("empty instance history response"))
	}
	if err != nil && resp.Status != platform.ResponseStatusPartialSuccess {
		return nil, nil, err
	}
	for _, h := range resp.Data.Instances {
		if h.Status != nil && *h.Status != platform.ResponseStatusSuccess {
			missing = append(missing, h.Uuid)
			continue
		}
		histories = append(histories, h)
	}
	return histories, missing, nil
}

// MissesHistory reports whether a missing history could change rows: a
// drawn instance loses its checkpoints, and a drawn checkpoint whose
// owner's history is missing hangs off a deleted placeholder instead.
func MissesHistory(missing []string, rows []Node) bool {
	if len(missing) == 0 {
		return false
	}
	drawn := make(map[string]bool, len(rows))
	for _, n := range rows {
		if n.Deleted {
			return true
		}
		drawn[n.UUID] = true
	}
	return slices.ContainsFunc(missing, func(id string) bool { return drawn[id] })
}
