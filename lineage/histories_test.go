// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package lineage

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unikraft.com/cloud/sdk/platform"
)

func TestSplitHistories(t *testing.T) {
	ok, failed := platform.ResponseStatusSuccess, platform.ResponseStatusError
	partial := &platform.Response[platform.GetCheckpointHistoryResponseData]{
		Status: platform.ResponseStatusPartialSuccess,
		Data: &platform.GetCheckpointHistoryResponseData{
			Instances: []platform.GetCheckpointHistoryResponseInstanceHistory{
				{Uuid: "a", Status: &ok},
				{Uuid: "b", Status: &failed},
				{Uuid: "c"},
			},
		},
	}
	boom := errors.New("boom")

	histories, missing, err := splitHistories(partial, boom)
	require.NoError(t, err, "a partial success is not a failure")
	require.Len(t, histories, 2)
	assert.Equal(t, "a", histories[0].Uuid)
	assert.Equal(t, "c", histories[1].Uuid, "an item without a status succeeded")
	assert.Equal(t, []string{"b"}, missing)

	_, _, err = splitHistories(&platform.Response[platform.GetCheckpointHistoryResponseData]{
		Status: platform.ResponseStatusError,
		Data:   partial.Data,
	}, boom)
	require.ErrorIs(t, err, boom, "an outright error drops the data")

	_, _, err = splitHistories(nil, boom)
	require.ErrorIs(t, err, boom)

	_, _, err = splitHistories(&platform.Response[platform.GetCheckpointHistoryResponseData]{}, nil)
	assert.Error(t, err, "a success without data is still empty")
}

func TestBatchHistories(t *testing.T) {
	require.Equal(t, 253, MaxOperations, "the platform rejects a batch of 254")

	uuids := make([]string, MaxOperations+2)
	for i := range uuids {
		uuids[i] = fmt.Sprint(i)
	}
	answer := func(batch []platform.NameOrUUID) *platform.Response[platform.GetCheckpointHistoryResponseData] {
		data := &platform.GetCheckpointHistoryResponseData{}
		for _, ref := range batch {
			data.Instances = append(data.Instances, platform.GetCheckpointHistoryResponseInstanceHistory{Uuid: *ref.Uuid})
		}
		return &platform.Response[platform.GetCheckpointHistoryResponseData]{Status: platform.ResponseStatusSuccess, Data: data}
	}

	var sizes []int
	read, err := batchHistories(uuids, func(batch []platform.NameOrUUID) (*platform.Response[platform.GetCheckpointHistoryResponseData], error) {
		sizes = append(sizes, len(batch))
		return answer(batch), nil
	})
	require.NoError(t, err)
	assert.Equal(t, []int{253, 2}, sizes, "no request is longer than the platform takes")
	assert.Len(t, read.Instances, len(uuids))
	assert.Empty(t, read.Missing)
	assert.Empty(t, read.Errs)

	even := make([]string, 2*MaxOperations)
	for i := range even {
		even[i] = fmt.Sprint(i)
	}
	sizes = nil
	read, err = batchHistories(even, func(batch []platform.NameOrUUID) (*platform.Response[platform.GetCheckpointHistoryResponseData], error) {
		sizes = append(sizes, len(batch))
		return answer(batch), nil
	})
	require.NoError(t, err)
	assert.Equal(t, []int{253, 253}, sizes, "a full last batch is not followed by an empty one")
	assert.Len(t, read.Instances, len(even))

	boom := errors.New("boom")
	read, err = batchHistories(uuids, func(batch []platform.NameOrUUID) (*platform.Response[platform.GetCheckpointHistoryResponseData], error) {
		if len(batch) == 2 {
			return nil, boom
		}
		return answer(batch), nil
	})
	require.NoError(t, err, "one failed batch leaves the others")
	assert.Len(t, read.Instances, MaxOperations)
	assert.Equal(t, []string{fmt.Sprint(MaxOperations), fmt.Sprint(MaxOperations + 1)}, read.Missing)
	require.Len(t, read.Errs, 1, "the failed batch says why")
	require.ErrorIs(t, read.Errs[0], boom)

	failed := platform.ResponseStatusError
	read, err = batchHistories(uuids, func(batch []platform.NameOrUUID) (*platform.Response[platform.GetCheckpointHistoryResponseData], error) {
		if len(batch) == 2 {
			return nil, boom
		}
		resp := answer(batch)
		for i := range resp.Data.Instances {
			resp.Data.Instances[i].Status = &failed
		}
		return resp, nil
	})
	require.NoError(t, err, "a batch that answered is not a failed batch")
	assert.Empty(t, read.Instances)
	assert.Len(t, read.Missing, len(uuids))
	assert.Len(t, read.Errs, 1)

	read, err = batchHistories(uuids, func([]platform.NameOrUUID) (*platform.Response[platform.GetCheckpointHistoryResponseData], error) {
		return nil, boom
	})
	require.ErrorIs(t, err, boom, "every batch failing fails the lineage")
	assert.Zero(t, read)
}

// historyClient answers GetInstanceHistory; any other call panics.
type historyClient struct {
	platform.Client
	calls [][]platform.NameOrUUID
}

func (c *historyClient) GetInstanceHistory(
	_ context.Context,
	refs []platform.NameOrUUID,
	_ platform.GetInstanceHistoryOpts,
) (*platform.Response[platform.GetCheckpointHistoryResponseData], error) {
	c.calls = append(c.calls, refs)
	data := &platform.GetCheckpointHistoryResponseData{}
	for _, ref := range refs {
		data.Instances = append(data.Instances, platform.GetCheckpointHistoryResponseInstanceHistory{Uuid: *ref.Uuid})
	}
	return &platform.Response[platform.GetCheckpointHistoryResponseData]{Status: platform.ResponseStatusSuccess, Data: data}, nil
}

func TestFetchHistories(t *testing.T) {
	client := &historyClient{}

	read, err := FetchHistories(t.Context(), client, nil)
	require.NoError(t, err)
	assert.Zero(t, read)
	assert.Empty(t, client.calls, "no instances make no request")

	read, err = FetchHistories(t.Context(), client, []platform.Instance{{Uuid: "a", Name: "alpha"}, {Uuid: "b", Name: "beta"}})
	require.NoError(t, err)
	require.Len(t, client.calls, 1)
	require.Len(t, client.calls[0], 2)
	assert.Equal(t, "a", *client.calls[0][0].Uuid)
	assert.Nil(t, client.calls[0][0].Name, "instances are asked for by UUID")
	assert.Equal(t, "b", *client.calls[0][1].Uuid)
	require.Len(t, read.Instances, 2)
	assert.Equal(t, "a", read.Instances[0].Uuid)
	assert.Equal(t, "b", read.Instances[1].Uuid)
}

func TestMissesHistory(t *testing.T) {
	rows := []Node{{UUID: "root"}, {UUID: "ck"}}
	withDeleted := append(rows, Node{UUID: DeletedPrefix + "gone", Deleted: true})

	assert.False(t, MissesHistory(nil, withDeleted), "nothing is missing")
	assert.False(t, MissesHistory([]string{"stranger"}, rows), "an unrelated instance does not matter")
	assert.True(t, MissesHistory([]string{"root"}, rows), "a drawn instance may lose checkpoints")
	assert.True(t, MissesHistory([]string{"stranger"}, withDeleted),
		"a deleted placeholder may stand in for the instance whose history is missing")
}
