// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client/ociindex"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/image-spec/reference"
	"unikraft.com/x/image-spec/schemes"
)

// writeJSON writes v to store as a blob of mediaType.
func writeJSON(t *testing.T, store content.Ingester, mediaType string, v any) ocispec.Descriptor {
	t.Helper()

	data, err := json.Marshal(v)
	require.NoError(t, err)
	desc := ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(data), Size: int64(len(data))}
	require.NoError(t, content.WriteBlob(t.Context(), store, desc.Digest.String(), bytes.NewReader(data), desc))
	return desc
}

// genericIndex writes to the layout at dir under tag an index of a linux image and its attestation, and returns it and the blobs of each.
func genericIndex(t *testing.T, dir, tag string) (ocispec.Descriptor, [][]ocispec.Descriptor) {
	t.Helper()
	ctx := t.Context()

	store, err := local.NewStore(dir)
	require.NoError(t, err)
	var entries []ocispec.Descriptor
	var blobs [][]ocispec.Descriptor
	for _, p := range []ocispec.Platform{{Architecture: "amd64", OS: "linux"}, {Architecture: "unknown", OS: "unknown"}} {
		// BuildKit gives an attestation the platform unknown/unknown, and marks its entry.
		attestation := p.OS == "unknown"
		data := []byte("layer of " + p.Architecture)
		layer := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: digest.FromBytes(data), Size: int64(len(data))}
		if attestation {
			layer.MediaType = "application/vnd.in-toto+json"
		}
		require.NoError(t, content.WriteBlob(ctx, store, layer.Digest.String(), bytes.NewReader(data), layer))
		config := writeJSON(t, store, ocispec.MediaTypeImageConfig, ocispec.Image{Platform: p})
		mfst := writeJSON(t, store, ocispec.MediaTypeImageManifest, ocispec.Manifest{
			SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest, Config: config, Layers: []ocispec.Descriptor{layer},
		})
		blobs = append(blobs, []ocispec.Descriptor{layer, config, mfst})
		mfst.Platform = &p
		if attestation {
			mfst.Annotations = map[string]string{annotationReferenceType: "attestation-manifest", "vnd.docker.reference.digest": entries[0].Digest.String()}
		}
		entries = append(entries, mfst)
	}
	idx := writeJSON(t, store, ocispec.MediaTypeImageIndex, ocispec.Index{
		SchemaVersion: 2, MediaType: ocispec.MediaTypeImageIndex, Manifests: entries,
		Annotations: map[string]string{"org.example": "kept"},
	})
	require.NoError(t, ociindex.NewStoreIndex(dir).Put(idx, ociindex.Tag(tag)))
	return idx, blobs
}

// layoutTar returns an OCI layout tarball with index and the blobs from store.
func layoutTar(t *testing.T, store content.Provider, index []byte, blobs ...ocispec.Descriptor) []byte {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(name string, data []byte) {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}))
		_, err := tw.Write(data)
		require.NoError(t, err)
	}
	add(ocispec.ImageLayoutFile, []byte(`{"imageLayoutVersion":"1.0.0"}`))
	add(ocispec.ImageIndexFile, index)
	for _, desc := range blobs {
		data, err := content.ReadBlob(t.Context(), store, desc)
		require.NoError(t, err)
		add("blobs/sha256/"+desc.Digest.Encoded(), data)
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

// requireLayoutHas checks that the layout at dir has each of descs.
func requireLayoutHas(t *testing.T, dir string, descs ...ocispec.Descriptor) {
	t.Helper()

	store, err := local.NewStore(dir)
	require.NoError(t, err)
	for _, desc := range descs {
		_, err := store.Info(t.Context(), desc.Digest)
		require.NoError(t, err, desc.Digest)
	}
}

func TestCopy(t *testing.T) {
	ctx := t.Context()
	reg, host, resolver := startTestRegistry(t)
	a := NewAccessor(WithResolver(resolver))

	src := t.TempDir()
	idx, blobs := genericIndex(t, src, "v1")
	all := append(append([]ocispec.Descriptor{idx}, blobs[0]...), blobs[1]...)

	t.Run("each location keeps every digest", func(t *testing.T) {
		pushed, err := a.Copy(ctx, Resolved{Location: Location{Scheme: schemes.OCILayout, Path: src + ":v1"}},
			&Location{Scheme: schemes.OCI, Path: host + "/acme/app:v1"}, nil)
		require.NoError(t, err)
		require.Equal(t, idx.Digest, pushed.Descriptor.Digest)
		require.Equal(t, idx.Digest, reg.tag("acme/app", "v1"))
		for _, desc := range all {
			require.True(t, reg.has(desc.Digest), desc.Digest)
		}

		tarball := filepath.Join(t.TempDir(), "app.tar")
		exported, err := a.Copy(ctx, pushed, &Location{Scheme: schemes.OCIArchive, Path: tarball}, nil)
		require.NoError(t, err)

		dst := t.TempDir()
		copied, err := a.Copy(ctx, Resolved{Location: Location{Scheme: schemes.OCIArchive, Path: tarball}},
			&Location{Scheme: schemes.OCILayout, Path: dst + ":v2"}, nil)
		require.NoError(t, err)
		require.Equal(t, idx.Digest, copied.Descriptor.Digest)
		requireLayoutHas(t, dst, all...)
		tagged, err := ociindex.NewStoreIndex(dst).Get("v2")
		require.NoError(t, err)
		require.Equal(t, idx.Digest, tagged.Digest)

		// What Copy returns loads again, without the attestation.
		again := t.TempDir()
		copied, err = a.Copy(ctx, exported, &Location{Scheme: schemes.OCILayout, Path: again + ":v3"}, nil)
		require.NoError(t, err)
		require.Equal(t, idx.Digest, copied.Descriptor.Digest)
		requireLayoutHas(t, again, all...)
		loaded, err := a.LoadAll(ctx, copied, platforms.All)
		require.NoError(t, err)
		require.Len(t, loaded, 1)
		require.Equal(t, blobs[0][2].Digest, loaded[0].ManifestDescriptor.Digest)
	})

	t.Run("a platform selects the manifests", func(t *testing.T) {
		dst := t.TempDir()
		linux := ocispec.Platform{Architecture: "amd64", OS: "linux"}
		copied, err := a.Copy(ctx, Resolved{Location: Location{Scheme: schemes.OCILayout, Path: src + ":v1"}},
			&Location{Scheme: schemes.OCILayout, Path: dst + ":v1"}, platforms.OnlyStrict(linux))
		require.NoError(t, err)

		// The copy has a new index over the manifest it kept, with the rest of the old one.
		require.NotEqual(t, idx.Digest, copied.Descriptor.Digest)
		data, err := content.ReadBlob(ctx, mustStore(t, dst), copied.Descriptor)
		require.NoError(t, err)
		var index ocispec.Index
		require.NoError(t, json.Unmarshal(data, &index))
		require.Len(t, index.Manifests, 1)
		require.Equal(t, blobs[0][2].Digest, index.Manifests[0].Digest)
		require.Equal(t, &linux, index.Manifests[0].Platform)
		require.Equal(t, map[string]string{"org.example": "kept"}, index.Annotations)
		requireLayoutHas(t, dst, blobs[0]...)
		for _, desc := range append([]ocispec.Descriptor{idx}, blobs[1]...) {
			_, err := mustStore(t, dst).Info(ctx, desc.Digest)
			require.Error(t, err, desc.Digest)
		}
		loaded, err := a.LoadAll(ctx, copied, platforms.All)
		require.NoError(t, err)
		require.Len(t, loaded, 1)
	})

	t.Run("a platform of every manifest keeps the index", func(t *testing.T) {
		dst := t.TempDir()
		copied, err := a.Copy(ctx, Resolved{Location: Location{Scheme: schemes.OCILayout, Path: src + ":v1"}},
			&Location{Scheme: schemes.OCILayout, Path: dst + ":v1"}, platforms.All)
		require.NoError(t, err)
		require.Equal(t, idx.Digest, copied.Descriptor.Digest)
		requireLayoutHas(t, dst, all...)
	})

	t.Run("a platform of no manifest copies nothing", func(t *testing.T) {
		_, err := a.Copy(ctx, Resolved{Location: Location{Scheme: schemes.OCILayout, Path: src + ":v1"}},
			&Location{Scheme: schemes.OCILayout, Path: t.TempDir() + ":v1"}, platforms.OnlyStrict(ocispec.Platform{Architecture: "riscv64", OS: "linux"}))
		require.ErrorIs(t, err, errdefs.ErrNotFound)
	})

	t.Run("a tarball of several images needs a descriptor", func(t *testing.T) {
		index, err := json.Marshal(ocispec.Index{SchemaVersion: 2, Manifests: []ocispec.Descriptor{blobs[0][2], blobs[1][2]}})
		require.NoError(t, err)
		tarball := filepath.Join(t.TempDir(), "several.tar")
		require.NoError(t, os.WriteFile(tarball, layoutTar(t, mustStore(t, src), index, append(blobs[0], blobs[1]...)...), 0o644))

		dst := &Location{Scheme: schemes.OCILayout, Path: t.TempDir() + ":v1"}
		_, err = a.Copy(ctx, Resolved{Location: Location{Scheme: schemes.OCIArchive, Path: tarball}}, dst, nil)
		require.ErrorContains(t, err, "lists 2 images")

		copied, err := a.Copy(ctx, Resolved{Location: Location{Scheme: schemes.OCIArchive, Path: tarball}, Descriptor: blobs[0][2]}, dst, nil)
		require.NoError(t, err)
		require.Equal(t, blobs[0][2].Digest, copied.Descriptor.Digest)
	})

	t.Run("a failed tarball is removed", func(t *testing.T) {
		// A root that is no manifest copies, but does not export.
		tarball := filepath.Join(t.TempDir(), "failed.tar")
		_, err := copyToTarball(ctx, tarball, mustStore(t, src), reference.Reference{}, blobs[0][1], nil)
		require.Error(t, err)
		require.NoFileExists(t, tarball)
	})

	t.Run("a tarball names its only image", func(t *testing.T) {
		var buf bytes.Buffer
		_, err := SaveTarballWriter(ctx, &buf, unikraftImage(fcIntel))
		require.NoError(t, err)
		saved := filepath.Join(t.TempDir(), "saved.tar")
		require.NoError(t, os.WriteFile(saved, buf.Bytes(), 0o644))

		pushed, err := a.Copy(ctx, Resolved{Location: Location{Scheme: schemes.OCIArchive, Path: saved}},
			&Location{Scheme: schemes.OCI, Path: host + "/acme/saved:v1"}, nil)
		require.NoError(t, err)
		require.Equal(t, ocispec.MediaTypeImageIndex, pushed.Descriptor.MediaType)
		loaded, err := a.LoadAll(ctx, pushed, platforms.All)
		require.NoError(t, err)
		require.Len(t, loaded, 1)
		require.NotNil(t, loaded[0].Kernel)
	})
}

// failingLayer fails the write of one blob after a manifest write or a delay.
type failingLayer struct {
	content.Ingester
	fail          digest.Digest
	manifest      chan struct{}
	once          sync.Once
	wroteManifest atomic.Bool
}

func (s *failingLayer) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	var wo content.WriterOpts
	for _, opt := range opts {
		if err := opt(&wo); err != nil {
			return nil, err
		}
	}
	if images.IsManifestType(wo.Desc.MediaType) || images.IsIndexType(wo.Desc.MediaType) {
		s.wroteManifest.Store(true)
		s.once.Do(func() { close(s.manifest) })
	}
	if wo.Desc.Digest == s.fail {
		select {
		case <-s.manifest:
		case <-time.After(500 * time.Millisecond):
		}
		return nil, errors.New("layer upload failed")
	}
	return s.Ingester.Writer(ctx, opts...)
}

// recording records the descriptors of the writers that a copy opens.
type recording struct {
	content.Ingester
	mu    sync.Mutex
	descs []ocispec.Descriptor
}

func (r *recording) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	var o content.WriterOpts
	for _, opt := range opts {
		if err := opt(&o); err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	r.descs = append(r.descs, o.Desc)
	r.mu.Unlock()
	return r.Ingester.Writer(ctx, opts...)
}

func TestCopyContent(t *testing.T) {
	ctx := t.Context()
	srcDir := t.TempDir()
	idx, blobs := genericIndex(t, srcDir, "v1")
	src, err := local.NewStore(srcDir)
	require.NoError(t, err)

	t.Run("manifests last", func(t *testing.T) {
		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		store := &failingLayer{Ingester: dst, fail: blobs[1][0].Digest, manifest: make(chan struct{})}
		_, err = copyContent(ctx, src, ingestRef(store, "latest"), reference.Reference{}, idx, nil)
		require.Error(t, err)
		require.False(t, store.wroteManifest.Load(), "a manifest was written before all blobs")
	})

	t.Run("reset", func(t *testing.T) {
		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		store := &resetting{Ingester: dst}
		_, err = copyContent(ctx, src, ingestRef(store, "latest"), reference.Reference{}, idx, nil)
		require.NoError(t, err)
		require.GreaterOrEqual(t, store.resets.Load(), int32(len(blobs[0])+len(blobs[1])+1))
		got, err := content.ReadBlob(ctx, dst, blobs[0][0])
		require.NoError(t, err)
		require.Equal(t, []byte("layer of amd64"), got)
	})

	t.Run("resumed", func(t *testing.T) {
		// An earlier copy left half of the layer in the store.
		layer := blobs[0][0]
		data, err := content.ReadBlob(ctx, src, layer)
		require.NoError(t, err)
		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		w, err := dst.Writer(ctx, content.WithRef("latest@"+layer.Digest.String()), content.WithDescriptor(layer))
		require.NoError(t, err)
		_, err = w.Write(data[:len(data)/2])
		require.NoError(t, err)
		require.NoError(t, w.Close())

		_, err = copyContent(ctx, src, ingestRef(dst, "latest"), reference.Reference{}, idx, nil)
		require.NoError(t, err)
		got, err := content.ReadBlob(ctx, dst, layer)
		require.NoError(t, err)
		require.Equal(t, data, got)
	})

	t.Run("a pusher mounts only from the source", func(t *testing.T) {
		// The manifest asks a pusher to mount the layer from another repository.
		store, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		data := []byte("layer")
		label := labels.LabelDistributionSource + ".index.unikraft.io"
		layer := ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageLayerGzip, Digest: digest.FromBytes(data), Size: int64(len(data)),
			Annotations: map[string]string{label: "victim/private"},
		}
		require.NoError(t, content.WriteBlob(ctx, store, layer.Digest.String(), bytes.NewReader(data), layer))
		config := writeJSON(t, store, ocispec.MediaTypeImageConfig, ocispec.Image{})
		mfst := writeJSON(t, store, ocispec.MediaTypeImageManifest, ocispec.Manifest{
			SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest, Config: config, Layers: []ocispec.Descriptor{layer},
		})
		source, err := reference.Parse("index.unikraft.io/acme/app:v1")
		require.NoError(t, err)

		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		rec := &recording{Ingester: dst}
		_, err = copyContent(ctx, store, ingestRef(rec, "latest"), source, mfst, nil)
		require.NoError(t, err)
		require.Len(t, rec.descs, 3)
		for _, desc := range rec.descs {
			require.Equal(t, map[string]string{label: "acme/app"}, desc.Annotations, desc.Digest)
		}
	})

	t.Run("an inline config", func(t *testing.T) {
		// The config is only in its descriptor, as the data field of OCI allows.
		store, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		raw, err := json.Marshal(ocispec.Image{Architecture: "amd64", OS: "linux"})
		require.NoError(t, err)
		config := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig, Digest: digest.FromBytes(raw), Size: int64(len(raw)), Data: raw}
		mfst := writeJSON(t, store, ocispec.MediaTypeImageManifest, ocispec.Manifest{
			SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest, Config: config, Layers: []ocispec.Descriptor{blobs[0][0]},
		})
		require.NoError(t, content.WriteBlob(ctx, store, blobs[0][0].Digest.String(), bytes.NewReader([]byte("layer of amd64")), blobs[0][0]))

		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		_, err = copyContent(ctx, store, ingestRef(dst, "latest"), reference.Reference{}, mfst, nil)
		require.NoError(t, err)
		got, err := content.ReadBlob(ctx, dst, ocispec.Descriptor{Digest: config.Digest, Size: config.Size})
		require.NoError(t, err)
		require.Equal(t, raw, got)
	})

	t.Run("no manifest as a blob", func(t *testing.T) {
		store, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		config := writeJSON(t, store, ocispec.MediaTypeImageConfig, ocispec.Image{})
		for _, mediaType := range []string{ocispec.MediaTypeImageManifest, ocispec.MediaTypeImageIndex, images.MediaTypeDockerSchema2Manifest, images.MediaTypeDockerSchema2ManifestList} {
			other := ocispec.Descriptor{MediaType: mediaType, Digest: idx.Digest, Size: idx.Size}
			for _, m := range []ocispec.Manifest{
				{SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest, Config: config, Layers: []ocispec.Descriptor{other}},
				{SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest, Config: other},
			} {
				mfst := writeJSON(t, store, ocispec.MediaTypeImageManifest, m)
				dst, err := local.NewStore(t.TempDir())
				require.NoError(t, err)
				_, err = copyContent(ctx, store, ingestRef(dst, "latest"), reference.Reference{}, mfst, nil)
				require.ErrorContains(t, err, mediaType)
			}
		}
	})

	t.Run("no foreign layers", func(t *testing.T) {
		store, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		config := writeJSON(t, store, ocispec.MediaTypeImageConfig, ocispec.Image{Architecture: "amd64", OS: "windows"})
		foreign := ocispec.Descriptor{
			MediaType: images.MediaTypeDockerSchema2LayerForeignGzip,
			Digest:    digest.FromString("foreign"),
			Size:      7,
			URLs:      []string{"https://example.com/foreign"},
		}
		mfst := writeJSON(t, store, ocispec.MediaTypeImageManifest, ocispec.Manifest{
			SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest, Config: config, Layers: []ocispec.Descriptor{foreign},
		})

		dir := t.TempDir()
		require.NoError(t, ociindex.NewStoreIndex(dir).Put(mfst, ociindex.Tag("v1")))
		_, err = copyContent(ctx, store, ingestRef(mustStore(t, dir), "v1"), reference.Reference{}, mfst, nil)
		require.NoError(t, err)
		requireLayoutHas(t, dir, config, mfst)
		_, err = mustStore(t, dir).Info(ctx, foreign.Digest)
		require.Error(t, err)

		_, err = NewAccessor().Copy(ctx, Resolved{Location: Location{Scheme: schemes.OCILayout, Path: dir + ":v1"}},
			&Location{Scheme: schemes.OCIArchive, Path: filepath.Join(t.TempDir(), "foreign.tar")}, nil)
		require.NoError(t, err)
	})
}

func mustStore(t *testing.T, dir string) content.Store {
	t.Helper()

	store, err := local.NewStore(dir)
	require.NoError(t, err)
	return store
}
