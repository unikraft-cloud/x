// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026, The BuildKit Authors.
// Licensed under the Apache License, Version 2.0 (the "License").
// You may not use this file except in compliance with the License.

package ociindex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/errdefs"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmptyDir(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreIndex(dir)
	idx, err := store.read()
	require.Error(t, err)
	assert.Nil(t, idx)
	require.ErrorIs(t, err, os.ErrNotExist)

	require.ErrorIs(t, store.Delete("ver1"), os.ErrNotExist)
	assert.NoFileExists(t, filepath.Join(dir, "index.json"))
}

func TestReadIndex(t *testing.T) {
	dir := t.TempDir()
	idx := ocispecs.Index{
		Manifests: []ocispecs.Descriptor{
			randDescriptor("foo"),
		},
	}
	dt, err := json.Marshal(idx)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(dir, "index.json"), dt, 0o644)
	require.NoError(t, err)

	store := NewStoreIndex(dir)
	readIdx, err := store.read()
	require.NoError(t, err)
	assert.Len(t, readIdx.Manifests, 1)

	assert.Equal(t, idx.Manifests[0], readIdx.Manifests[0])
}

func TestIndexTooLarge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	err := os.WriteFile(path, append(bytes.Repeat([]byte(" "), int(docker.MaxManifestSize)), "{}"...), 0o644)
	require.NoError(t, err)

	store := NewStoreIndex(dir)
	_, err = store.read()
	require.ErrorIs(t, err, ErrTooLarge)
	require.EqualError(t, err, path+" is larger than 4.2 MiB")
	require.ErrorIs(t, store.Put(randDescriptor("foo"), "ver1"), ErrTooLarge)
	require.ErrorIs(t, store.Delete("ver1"), ErrTooLarge)

	err = os.WriteFile(path, append(bytes.Repeat([]byte(" "), int(docker.MaxManifestSize)-2), "{]"...), 0o644)
	require.NoError(t, err)
	_, err = store.read()
	require.EqualError(t, err, "could not unmarshal "+path+": invalid character ']' looking for beginning of object key string")

	err = os.WriteFile(path, append(bytes.Repeat([]byte(" "), int(docker.MaxManifestSize)-2), "{}"...), 0o644)
	require.NoError(t, err)
	require.NoError(t, store.Put(randDescriptor("foo"), "ver1"))
}

func TestReadByTag(t *testing.T) {
	dir := t.TempDir()

	one := randDescriptor("foo")
	two := randDescriptor("bar")
	three := randDescriptor("baz")

	const refName = "org.opencontainers.image.ref.name"

	two.Annotations = map[string]string{
		refName: "ver1",
	}
	three.Annotations = map[string]string{
		refName: "ver2",
	}

	idx := ocispecs.Index{
		Manifests: []ocispecs.Descriptor{
			one,
			two,
			three,
		},
	}
	dt, err := json.Marshal(idx)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(dir, "index.json"), dt, 0o644)
	require.NoError(t, err)

	store := NewStoreIndex(dir)
	desc, err := store.Get("ver1")
	require.NoError(t, err)

	assert.Equal(t, *desc, two)

	desc, err = store.Get("ver3")
	require.NoError(t, err)
	assert.Nil(t, desc)
}

func TestWriteSingleDescriptor(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreIndex(dir)

	desc := randDescriptor("foo")
	err := store.Put(desc, "ver1")
	require.NoError(t, err)

	readDesc, err := store.GetSingle()
	require.NoError(t, err)
	assert.Equal(t, desc.Digest, readDesc.Digest)
}

func TestAddDescriptorWithTag(t *testing.T) {
	dir := t.TempDir()

	one := randDescriptor("foo")
	two := randDescriptor("bar")

	idx := ocispecs.Index{
		Manifests: []ocispecs.Descriptor{
			one,
			two,
		},
	}
	dt, err := json.Marshal(idx)
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(dir, "index.json"), dt, 0o644)
	require.NoError(t, err)

	store := NewStoreIndex(dir)
	three := randDescriptor("baz")
	err = store.Put(three, "ver1")
	require.NoError(t, err)

	desc, err := store.Get("ver1")
	require.NoError(t, err)
	assert.Equal(t, three.Digest, desc.Digest)

	readIdx, err := store.read()
	require.NoError(t, err)

	assert.Len(t, readIdx.Manifests, 3)
	assert.Equal(t, one, readIdx.Manifests[0])
	assert.Equal(t, two, readIdx.Manifests[1])
	assert.Equal(t, three.Digest, readIdx.Manifests[2].Digest)
	assert.Equal(t, map[string]string{"org.opencontainers.image.ref.name": "ver1"}, readIdx.Manifests[2].Annotations)

	// store.Put also sets defaults for MediaType and SchemaVersion
	assert.Equal(t, ocispecs.MediaTypeImageIndex, readIdx.MediaType)
	assert.Equal(t, 2, readIdx.SchemaVersion)
}

func TestReplaceByTag(t *testing.T) {
	dir := t.TempDir()

	store := NewStoreIndex(dir)
	one := randDescriptor("foo")
	two := randDescriptor("bar")
	three := randDescriptor("baz")

	err := store.Put(one, "ver1")
	require.NoError(t, err)

	err = store.Put(two, "ver2")
	require.NoError(t, err)

	err = store.Put(three, "ver1")
	require.NoError(t, err)

	readIdx, err := store.read()
	require.NoError(t, err)

	assert.Len(t, readIdx.Manifests, 2)

	assert.Equal(t, two.Digest, readIdx.Manifests[0].Digest)
	assert.Equal(t, "ver2", readIdx.Manifests[0].Annotations["org.opencontainers.image.ref.name"])

	assert.Equal(t, three.Digest, readIdx.Manifests[1].Digest)
	assert.Equal(t, "ver1", readIdx.Manifests[1].Annotations["org.opencontainers.image.ref.name"])
}

func TestReplaceNamedEntry(t *testing.T) {
	dir := t.TempDir()

	one := randDescriptor("foo")
	one.Annotations = map[string]string{
		"io.containerd.image.name":          "docker.io/library/app:v1",
		"org.opencontainers.image.ref.name": "v1",
	}
	two := randDescriptor("bar")
	two.Annotations = map[string]string{
		"io.containerd.image.name": "v1",
	}

	dt, err := json.Marshal(ocispecs.Index{Manifests: []ocispecs.Descriptor{one, two}})
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(dir, "index.json"), dt, 0o644)
	require.NoError(t, err)

	store := NewStoreIndex(dir)
	three := randDescriptor("baz")
	err = store.Put(three, "v1")
	require.NoError(t, err)

	readIdx, err := store.read()
	require.NoError(t, err)
	require.Len(t, readIdx.Manifests, 2)
	assert.Equal(t, two, readIdx.Manifests[0])
	assert.Equal(t, three.Digest, readIdx.Manifests[1].Digest)
}

func TestDelete(t *testing.T) {
	dir := t.TempDir()

	store := NewStoreIndex(dir)
	require.NoError(t, store.Put(randDescriptor("foo"), "ver1"))
	require.NoError(t, store.Put(randDescriptor("bar"), "ver2"))

	require.NoError(t, store.Delete("ver1"))

	readIdx, err := store.read()
	require.NoError(t, err)
	require.Len(t, readIdx.Manifests, 1)
	assert.Equal(t, "ver2", readIdx.Manifests[0].Annotations["org.opencontainers.image.ref.name"])

	require.ErrorIs(t, store.Delete("ver1"), errdefs.ErrNotFound)
	require.Error(t, store.Delete(""))

	readIdx, err = store.read()
	require.NoError(t, err)
	assert.Len(t, readIdx.Manifests, 1)
}

func randDescriptor(seed string) ocispecs.Descriptor {
	dgst := digest.FromBytes([]byte(seed))
	return ocispecs.Descriptor{
		MediaType: "application/vnd.test.descriptor+json",
		Digest:    dgst,
		Size:      int64(len(seed)),
	}
}
