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
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/image-spec/contentutil"
	"unikraft.com/x/image-spec/ociindex"
	"unikraft.com/x/image-spec/reference"
	"unikraft.com/x/image-spec/schemes"
)

// annotationReferenceType marks an index entry that is no image, such as an attestation of BuildKit.
const annotationReferenceType = "vnd.docker.reference.type"

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
	require.NoError(t, ociindex.NewStoreIndex(dir).Put(idx, tag))
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
	linux := ocispec.Platform{Architecture: "amd64", OS: "linux"}

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
		require.Equal(t, idx.Digest, exported.Descriptor.Digest)

		dst := t.TempDir()
		copied, err := a.Copy(ctx, Resolved{Location: Location{Scheme: schemes.OCIArchive, Path: tarball}},
			&Location{Scheme: schemes.OCILayout, Path: dst + ":v2"}, nil)
		require.NoError(t, err)
		require.Equal(t, idx.Digest, copied.Descriptor.Digest)
		requireLayoutHas(t, dst, all...)
		tagged, err := ociindex.NewStoreIndex(dst).Get("v2")
		require.NoError(t, err)
		require.Equal(t, idx.Digest, tagged.Digest)

		// What Copy returns copies and loads again.
		again := t.TempDir()
		copied, err = a.Copy(ctx, exported, &Location{Scheme: schemes.OCILayout, Path: again + ":v3"}, nil)
		require.NoError(t, err)
		require.Equal(t, idx.Digest, copied.Descriptor.Digest)
		requireLayoutHas(t, again, all...)
		loaded, err := a.Load(ctx, copied, platforms.OnlyStrict(linux))
		require.NoError(t, err)
		require.Equal(t, blobs[0][2].Digest, loaded.ManifestDescriptor.Digest)
	})

	t.Run("a platform selects the manifests", func(t *testing.T) {
		dst := t.TempDir()
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

	t.Run("a tarball of several images needs a tag or a descriptor", func(t *testing.T) {
		tagged := func(desc ocispec.Descriptor, tag string) ocispec.Descriptor {
			desc.Annotations = map[string]string{ocispec.AnnotationRefName: tag}
			return desc
		}
		index, err := json.Marshal(ocispec.Index{SchemaVersion: 2, Manifests: []ocispec.Descriptor{tagged(blobs[0][2], "v1"), tagged(blobs[1][2], "v2")}})
		require.NoError(t, err)
		tarball := filepath.Join(t.TempDir(), "several.tar")
		require.NoError(t, os.WriteFile(tarball, layoutTar(t, mustStore(t, src), index, append(blobs[0], blobs[1]...)...), 0o644))

		dst := &Location{Scheme: schemes.OCILayout, Path: t.TempDir() + ":v1"}
		_, err = a.Copy(ctx, Resolved{Location: Location{Scheme: schemes.OCIArchive, Path: tarball}}, dst, nil)
		require.ErrorContains(t, err, "give a tag")

		copied, err := a.Copy(ctx, Resolved{Location: Location{Scheme: schemes.OCIArchive, Path: tarball + ":v2"}}, dst, nil)
		require.NoError(t, err)
		require.Equal(t, blobs[1][2].Digest, copied.Descriptor.Digest)

		copied, err = a.Copy(ctx, Resolved{Location: Location{Scheme: schemes.OCIArchive, Path: tarball}, Descriptor: blobs[0][2]}, dst, nil)
		require.NoError(t, err)
		require.Equal(t, blobs[0][2].Digest, copied.Descriptor.Digest)
	})

	t.Run("an index.json that is the image index is copied as a blob", func(t *testing.T) {
		layout, index := manifestLayout(t, fcIntel, fcArm)
		dst := t.TempDir()
		copied, err := a.Copy(ctx, Resolved{Location: Location{Scheme: schemes.OCILayout, Path: layout}},
			&Location{Scheme: schemes.OCILayout, Path: dst}, nil)
		require.NoError(t, err)
		require.Equal(t, ociindex.Descriptor(index).Digest, copied.Descriptor.Digest)
		requireLayoutHas(t, dst, copied.Descriptor)
	})

	t.Run("a tarball with other tags is not rewritten", func(t *testing.T) {
		from := Resolved{Location: Location{Scheme: schemes.OCILayout, Path: src + ":v1"}}
		tarball := filepath.Join(t.TempDir(), "app.tar")
		_, err := a.Copy(ctx, from, &Location{Scheme: schemes.OCIArchive, Path: tarball + ":v1"}, nil)
		require.NoError(t, err)
		_, err = a.Copy(ctx, from, &Location{Scheme: schemes.OCIArchive, Path: tarball + ":v2"}, nil)
		require.ErrorContains(t, err, "holds other images")

		copied, err := a.Copy(ctx, Resolved{Location: Location{Scheme: schemes.OCIArchive, Path: tarball + ":v1"}},
			&Location{Scheme: schemes.OCILayout, Path: t.TempDir()}, nil)
		require.NoError(t, err)
		require.Equal(t, idx.Digest, copied.Descriptor.Digest)
	})

	t.Run("a failed tarball is removed", func(t *testing.T) {
		tarball := filepath.Join(t.TempDir(), "failed.tar")
		_, err := a.Copy(ctx, Resolved{Location: Location{Scheme: schemes.OCILayout, Path: src + ":v1"}},
			&Location{Scheme: schemes.OCIArchive, Path: tarball}, platforms.OnlyStrict(ocispec.Platform{Architecture: "riscv64", OS: "linux"}))
		require.ErrorIs(t, err, errdefs.ErrNotFound)
		require.NoFileExists(t, tarball)
	})
}

// platformManifest writes to store a manifest of p with one layer, and returns it with all its blobs.
func platformManifest(t *testing.T, store content.Ingester, p ocispec.Platform) (ocispec.Descriptor, []ocispec.Descriptor) {
	t.Helper()

	data := []byte("layer of " + platforms.Format(p))
	layer := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: digest.FromBytes(data), Size: int64(len(data))}
	require.NoError(t, content.WriteBlob(t.Context(), store, layer.Digest.String(), bytes.NewReader(data), layer))
	config := writeJSON(t, store, ocispec.MediaTypeImageConfig, ocispec.Image{Platform: p})
	mfst := writeJSON(t, store, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest, Config: config, Layers: []ocispec.Descriptor{layer},
	})
	return mfst, []ocispec.Descriptor{mfst, config, layer}
}

func TestCopyPlatform(t *testing.T) {
	ctx := t.Context()
	src := t.TempDir()
	store := mustStore(t, src)
	a := NewAccessor()

	amd64 := ocispec.Platform{OS: "linux", Architecture: "amd64"}
	arm64 := ocispec.Platform{OS: "linux", Architecture: "arm64"}
	withFeatures := ocispec.Platform{OS: "linux", Architecture: "amd64", OSFeatures: []string{"feature"}}
	amd64Mfst, amd64Blobs := platformManifest(t, store, amd64)
	arm64Mfst, arm64Blobs := platformManifest(t, store, arm64)
	labelled := func(desc ocispec.Descriptor, p ocispec.Platform) ocispec.Descriptor {
		desc.Platform = &p
		return desc
	}
	index := func(entries ...ocispec.Descriptor) ocispec.Descriptor {
		return writeJSON(t, store, ocispec.MediaTypeImageIndex, ocispec.Index{SchemaVersion: 2, MediaType: ocispec.MediaTypeImageIndex, Manifests: entries})
	}
	amd64Index := index(labelled(amd64Mfst, amd64))
	arm64Index := index(labelled(arm64Mfst, arm64))

	for _, tc := range []struct {
		name string
		root ocispec.Descriptor
		kept ocispec.Descriptor   // the only entry of the new root
		has  []ocispec.Descriptor // what the copy has
	}{{
		name: "a sub-index without a match is dropped",
		root: index(arm64Index, amd64Index),
		kept: amd64Index,
		has:  append([]ocispec.Descriptor{amd64Index}, amd64Blobs...),
	}, {
		name: "an entry without a platform matches by its config",
		root: index(arm64Mfst, labelled(amd64Mfst, amd64)),
		kept: labelled(amd64Mfst, amd64),
		has:  amd64Blobs,
	}, {
		name: "an entry matches whatever its os.features",
		root: index(labelled(arm64Mfst, arm64), labelled(amd64Mfst, withFeatures)),
		kept: labelled(amd64Mfst, withFeatures),
		has:  amd64Blobs,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, ociindex.NewStoreIndex(src).Put(tc.root, "v1"))
			from := Resolved{Location: Location{Scheme: schemes.OCILayout, Path: src + ":v1"}}
			want, err := a.LoadAll(ctx, from, platforms.OnlyStrict(amd64))
			require.NoError(t, err)
			require.Len(t, want, 1)

			for _, dest := range []*Location{
				{Scheme: schemes.OCILayout, Path: t.TempDir() + ":v1"},
				{Scheme: schemes.OCIArchive, Path: filepath.Join(t.TempDir(), "app.tar")},
			} {
				copied, err := a.Copy(ctx, from, dest, platforms.OnlyStrict(amd64))
				require.NoError(t, err, dest.Scheme)
				got, err := a.LoadAll(ctx, copied, platforms.All)
				require.NoError(t, err, dest.Scheme)
				require.Len(t, got, 1, dest.Scheme)
				require.Equal(t, want[0].ManifestDescriptor.Digest, got[0].ManifestDescriptor.Digest, dest.Scheme)
				if dest.Scheme != schemes.OCILayout {
					continue
				}

				dir, _ := splitPathTag(dest.Path)
				data, err := content.ReadBlob(ctx, mustStore(t, dir), copied.Descriptor)
				require.NoError(t, err)
				var idx ocispec.Index
				require.NoError(t, json.Unmarshal(data, &idx))
				require.Equal(t, []ocispec.Descriptor{tc.kept}, idx.Manifests)
				requireLayoutHas(t, dir, tc.has...)
				for _, desc := range append([]ocispec.Descriptor{arm64Index}, arm64Blobs...) {
					_, err := mustStore(t, dir).Info(ctx, desc.Digest)
					require.Error(t, err, desc.Digest)
				}
			}
		})
	}

	t.Run("only a root that keeps nothing is not found", func(t *testing.T) {
		require.NoError(t, ociindex.NewStoreIndex(src).Put(index(arm64Index), "v1"))
		_, err := a.Copy(ctx, Resolved{Location: Location{Scheme: schemes.OCILayout, Path: src + ":v1"}},
			&Location{Scheme: schemes.OCILayout, Path: t.TempDir() + ":v1"}, platforms.OnlyStrict(amd64))
		require.ErrorIs(t, err, errdefs.ErrNotFound)
	})
}

func TestCopyToRegistry(t *testing.T) {
	src := t.TempDir()
	idx, blobs := genericIndex(t, src, "v1")
	from := Resolved{Location: Location{Scheme: schemes.OCILayout, Path: src + ":v1"}}

	t.Run("each copy writes its tag", func(t *testing.T) {
		ctx := t.Context()
		reg, host, resolver := startTestRegistry(t)
		a := NewAccessor(WithResolver(resolver))

		for _, ref := range []string{"acme/app:one", "acme/app:two", "acme/other:one"} {
			_, err := a.Copy(ctx, from, &Location{Scheme: schemes.OCI, Path: host + "/" + ref}, nil)
			require.NoError(t, err, ref)
		}
		require.Equal(t, idx.Digest, reg.tag("acme/app", "one"))
		require.Equal(t, idx.Digest, reg.tag("acme/app", "two"))
		require.Equal(t, idx.Digest, reg.tag("acme/other", "one"))

		img := unikraftImage(fcIntel)
		one, err := a.Save(ctx, &Location{Scheme: schemes.OCI, Path: host + "/acme/saved:one"}, img)
		require.NoError(t, err)
		_, err = a.Save(ctx, &Location{Scheme: schemes.OCI, Path: host + "/acme/saved:two"}, img)
		require.NoError(t, err)
		require.Equal(t, one.Descriptor.Digest, reg.tag("acme/saved", "one"))
		require.Equal(t, one.Descriptor.Digest, reg.tag("acme/saved", "two"))
	})

	t.Run("only the root goes to the tag", func(t *testing.T) {
		reg, host, resolver := startTestRegistry(t)
		_, err := NewAccessor(WithResolver(resolver)).Copy(t.Context(), from, &Location{Scheme: schemes.OCI, Path: host + "/acme/app:v1"}, nil)
		require.NoError(t, err)

		puts := reg.manifestPuts()
		require.Len(t, puts, 3)
		require.ElementsMatch(t, []string{blobs[0][2].Digest.String(), blobs[1][2].Digest.String()}, puts[:2])
		require.Equal(t, "v1", puts[2])
	})

	t.Run("only the new root goes to the tag", func(t *testing.T) {
		reg, host, resolver := startTestRegistry(t)
		linux := platforms.OnlyStrict(ocispec.Platform{Architecture: "amd64", OS: "linux"})
		pushed, err := NewAccessor(WithResolver(resolver)).Copy(t.Context(), from, &Location{Scheme: schemes.OCI, Path: host + "/acme/app:v1"}, linux)
		require.NoError(t, err)

		require.Equal(t, []string{blobs[0][2].Digest.String(), "v1"}, reg.manifestPuts())
		require.Equal(t, pushed.Descriptor.Digest, reg.tag("acme/app", "v1"))
	})

	t.Run("a failed root leaves the tag", func(t *testing.T) {
		ctx := t.Context()
		reg, host, resolver := startTestRegistry(t)
		a := NewAccessor(WithResolver(resolver))
		dest := &Location{Scheme: schemes.OCI, Path: host + "/acme/app:v1"}
		old, err := a.Save(ctx, dest, unikraftImage(fcIntel))
		require.NoError(t, err)

		reg.refuse(idx.Digest)
		_, err = a.Copy(ctx, from, dest, nil)
		require.ErrorContains(t, err, "400 Bad Request")
		require.Equal(t, old.Descriptor.Digest, reg.tag("acme/app", "v1"))
		require.True(t, reg.has(blobs[0][2].Digest))
	})
}

// resettingMidWrite is an ingester whose writers reset once halfway through
// their first write, as a pusher does when it re-sends an upload.
type resettingMidWrite struct {
	content.Ingester
	resets atomic.Int32
}

func (i *resettingMidWrite) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	w, err := i.Ingester.Writer(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &midWriteResetter{Writer: w, resets: &i.resets}, nil
}

type midWriteResetter struct {
	content.Writer
	reset  bool
	resets *atomic.Int32
}

func (w *midWriteResetter) Write(p []byte) (int, error) {
	if w.reset || len(p) < 2 {
		return w.Writer.Write(p)
	}
	w.reset = true
	w.resets.Add(1)
	n, err := w.Writer.Write(p[:len(p)/2])
	if err != nil {
		return n, err
	}
	if err := w.Truncate(0); err != nil {
		return n, err
	}
	return n, content.ErrReset
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
		rec := &recording{Ingester: mustStore(t, t.TempDir())}
		dst := ingestRef(rec, "latest")
		_, err := copyContent(ctx, src, dst, dst, reference.Reference{}, idx, nil)
		require.NoError(t, err)

		digests := func(descs ...ocispec.Descriptor) []digest.Digest {
			var out []digest.Digest
			for _, desc := range descs {
				out = append(out, desc.Digest)
			}
			return out
		}
		// Every blob is written before the manifests, and the index last.
		require.Len(t, rec.descs, 7)
		require.ElementsMatch(t, digests(blobs[0][0], blobs[0][1], blobs[1][0], blobs[1][1]), digests(rec.descs[:4]...))
		require.ElementsMatch(t, digests(blobs[0][2], blobs[1][2]), digests(rec.descs[4:6]...))
		require.Equal(t, idx.Digest, rec.descs[6].Digest)
	})

	t.Run("reset", func(t *testing.T) {
		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		store := &resetting{Ingester: dst}
		_, err = copyContent(ctx, src, ingestRef(store, "latest"), ingestRef(store, "latest"), reference.Reference{}, idx, nil)
		require.NoError(t, err)
		require.GreaterOrEqual(t, store.resets.Load(), int32(len(blobs[0])+len(blobs[1])+1))
		got, err := content.ReadBlob(ctx, dst, blobs[0][0])
		require.NoError(t, err)
		require.Equal(t, []byte("layer of amd64"), got)
	})

	t.Run("reset mid-upload", func(t *testing.T) {
		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		store := &resettingMidWrite{Ingester: dst}
		_, err = copyContent(ctx, src, ingestRef(store, "latest"), ingestRef(store, "latest"), reference.Reference{}, idx, nil)
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

		_, err = copyContent(ctx, src, ingestRef(dst, "latest"), ingestRef(dst, "latest"), reference.Reference{}, idx, nil)
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
		_, err = copyContent(ctx, store, ingestRef(rec, "latest"), ingestRef(rec, "latest"), source, mfst, nil)
		require.NoError(t, err)
		require.Len(t, rec.descs, 3)
		for _, desc := range rec.descs {
			require.Equal(t, map[string]string{label: "acme/app"}, desc.Annotations, desc.Digest)
		}
	})

	t.Run("an invalid digest writes nothing", func(t *testing.T) {
		// The digest of the layer would put a query in the request of a pusher.
		store, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		layer := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: digest.Digest(blobs[0][0].Digest.String() + "&from=victim/private#"), Size: 5}
		config := writeJSON(t, store, ocispec.MediaTypeImageConfig, ocispec.Image{})
		mfst := writeJSON(t, store, ocispec.MediaTypeImageManifest, ocispec.Manifest{
			SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest, Config: config, Layers: []ocispec.Descriptor{layer},
		})

		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		rec := &recording{Ingester: dst}
		_, err = copyContent(ctx, store, ingestRef(rec, "latest"), ingestRef(rec, "latest"), reference.Reference{}, mfst, nil)
		require.ErrorIs(t, err, digest.ErrDigestInvalidLength)
		require.Empty(t, rec.descs)
	})

	t.Run("a save of an invalid digest writes nothing", func(t *testing.T) {
		// A loaded image can have such a layer, as Load does not read the layers.
		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		rec := &recording{Ingester: dst}
		layer := blobs[0][0]
		layer.Digest = digest.Digest(layer.Digest.String() + "&from=victim/private#")
		_, err = packageCopy(ctx, ingestRef(rec, "latest"), reference.Reference{}, src, layer)
		require.ErrorIs(t, err, digest.ErrDigestInvalidLength)
		require.Empty(t, rec.descs)
	})

	t.Run("a root that is no manifest or index", func(t *testing.T) {
		dst, err := local.NewStore(t.TempDir())
		require.NoError(t, err)
		rec := &recording{Ingester: dst}
		root := idx
		root.MediaType = ""
		_, err = copyContent(ctx, src, ingestRef(rec, "latest"), ingestRef(rec, "latest"), reference.Reference{}, root, nil)
		require.ErrorContains(t, err, "not that of a manifest or an index")
		require.Empty(t, rec.descs)
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
		_, err = copyContent(ctx, store, ingestRef(dst, "latest"), ingestRef(dst, "latest"), reference.Reference{}, mfst, nil)
		require.NoError(t, err)
		got, err := content.ReadBlob(ctx, dst, ocispec.Descriptor{Digest: config.Digest, Size: config.Size})
		require.NoError(t, err)
		require.Equal(t, raw, got)
	})

	t.Run("a child too large to read", func(t *testing.T) {
		// A registry gives the size of a blob only in its parent.
		mfst := blobs[0][2]
		data, err := content.ReadBlob(ctx, src, mfst)
		require.NoError(t, err)
		huge := mfst
		huge.Size = 1 << 40
		index, err := json.Marshal(ocispec.Index{SchemaVersion: 2, MediaType: ocispec.MediaTypeImageIndex, Manifests: []ocispec.Descriptor{huge}})
		require.NoError(t, err)
		root := describe(index)
		root.MediaType = ocispec.MediaTypeImageIndex
		provider := contentutil.FromFetcher(served{root.Digest: index, mfst.Digest: data})

		dst := ingestRef(mustStore(t, t.TempDir()), "latest")
		_, err = copyContent(ctx, provider, dst, dst, reference.Reference{}, root, nil)
		require.ErrorContains(t, err, fmt.Sprintf("blob %s: size %d is not between", mfst.Digest, huge.Size))
	})

	t.Run("no manifest as a blob", func(t *testing.T) {
		store := mustStore(t, t.TempDir())
		config := writeJSON(t, store, ocispec.MediaTypeImageConfig, ocispec.Image{})
		mfst := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: idx.Digest, Size: idx.Size}
		index := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageIndex, Digest: idx.Digest, Size: idx.Size}
		for _, tc := range []struct {
			other ocispec.Descriptor
			m     ocispec.Manifest
		}{
			{other: mfst, m: ocispec.Manifest{SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest, Config: config, Layers: []ocispec.Descriptor{mfst}}},
			{other: index, m: ocispec.Manifest{SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest, Config: index}},
		} {
			root := writeJSON(t, store, ocispec.MediaTypeImageManifest, tc.m)
			dst := ingestRef(mustStore(t, t.TempDir()), "latest")
			_, err := copyContent(ctx, store, dst, dst, reference.Reference{}, root, nil)
			require.ErrorContains(t, err, tc.other.MediaType)
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
		require.NoError(t, ociindex.NewStoreIndex(dir).Put(mfst, "v1"))
		layout := ingestRef(mustStore(t, dir), "v1")
		_, err = copyContent(ctx, store, layout, layout, reference.Reference{}, mfst, nil)
		require.NoError(t, err)
		requireLayoutHas(t, dir, config, mfst)
		_, err = mustStore(t, dir).Info(ctx, foreign.Digest)
		require.Error(t, err)

		_, err = NewAccessor().Copy(ctx, Resolved{Location: Location{Scheme: schemes.OCILayout, Path: dir + ":v1"}},
			&Location{Scheme: schemes.OCIArchive, Path: filepath.Join(t.TempDir(), "foreign.tar")}, nil)
		require.NoError(t, err)
	})
}
