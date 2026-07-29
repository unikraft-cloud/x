// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

// layerStore holds a hand-built image, so that a test can describe a layer this
// version was never taught about.
type layerStore struct {
	t     *testing.T
	store content.Store
}

func newLayerStore(t *testing.T) *layerStore {
	t.Helper()

	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)

	return &layerStore{t: t, store: store}
}

// put writes a blob and describes it.
func (s *layerStore) put(mediaType string, body []byte) ocispec.Descriptor {
	s.t.Helper()

	desc := ocispec.Descriptor{
		MediaType: mediaType,
		Digest:    digest.FromBytes(body),
		Size:      int64(len(body)),
	}

	w, err := s.store.Writer(s.t.Context(), content.WithRef(desc.Digest.String()))
	require.NoError(s.t, err)

	_, err = w.Write(body)
	require.NoError(s.t, err)
	require.NoError(s.t, w.Commit(s.t.Context(), desc.Size, desc.Digest))
	require.NoError(s.t, w.Close())

	return desc
}

// image writes a manifest with the given layers and describes it.
func (s *layerStore) image(layers ...ocispec.Descriptor) ocispec.Descriptor {
	s.t.Helper()

	config := ocispec.Image{Platform: ocispec.Platform{OS: "kraftcloud", Architecture: "x86_64"}}

	rawConfig, err := json.Marshal(config)
	require.NoError(s.t, err)

	rawManifest, err := json.Marshal(ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    s.put(ocispec.MediaTypeImageConfig, rawConfig),
		Layers:    layers,
	})
	require.NoError(s.t, err)

	return s.put(ocispec.MediaTypeImageManifest, rawManifest)
}

func (s *layerStore) load(ctx context.Context, desc ocispec.Descriptor) (*Image, error) {
	return LoadContent(ctx, s.store, desc, platforms.All)
}

// TestLoadReadsAKernelNamedByItsMediaType covers a layer that says what it is
// rather than carrying an annotation to say it.
func TestLoadReadsAKernelNamedByItsMediaType(t *testing.T) {
	s := newLayerStore(t)

	desc := s.image(
		s.put(MediaTypeKernel, []byte("kernel data")),
		s.put(MediaTypeInitrd, []byte("initrd data")),
		s.put(MediaTypeRom, []byte("rom data")),
	)

	img, err := s.load(t.Context(), desc)
	require.NoError(t, err)

	require.NotNil(t, img.Kernel)
	require.NotNil(t, img.Initrd)
	require.Len(t, img.Roms, 1)

	require.Equal(t, WellKnownKernelPath, img.Kernel.Path())
	require.Equal(t, WellKnownInitrdPath, img.Initrd.Path())
}

// TestLoadKeepsAPathALayerAsksFor covers a layer naming both its kind and
// where it goes, where the second still decides.
func TestLoadKeepsAPathALayerAsksFor(t *testing.T) {
	s := newLayerStore(t)

	kernel := s.put(MediaTypeKernel, []byte("kernel data"))
	kernel.Annotations = map[string]string{AnnotationKernelPath: "/somewhere/else"}

	img, err := s.load(t.Context(), s.image(kernel))
	require.NoError(t, err)
	require.Equal(t, "/somewhere/else", img.Kernel.Path())
}

// TestLoadStillReadsAnAnnotatedKernel covers every image written so far, which
// names its kernel with an annotation on an ordinary layer.
func TestLoadStillReadsAnAnnotatedKernel(t *testing.T) {
	s := newLayerStore(t)

	kernel := s.put(ocispec.MediaTypeImageLayer, []byte("kernel data"))
	kernel.Annotations = map[string]string{AnnotationKernelPath: WellKnownKernelPath}

	initrd := s.put(ocispec.MediaTypeImageLayer, []byte("initrd data"))
	initrd.Annotations = map[string]string{AnnotationKernelInitrdPath: WellKnownInitrdPath}

	img, err := s.load(t.Context(), s.image(kernel, initrd))
	require.NoError(t, err)

	require.NotNil(t, img.Kernel)
	require.NotNil(t, img.Initrd)
}

// TestLoadRefusesALayerOfOursItDoesNotUnderstand is the assertion this whole
// migration rests on.
//
// A reader older than the image it is given used to skip the layer, hand back
// an image with no kernel and no error, and leave its caller to boot nothing at
// all. That is the worst available failure: silent, and only discovered by a
// machine that does not start. It has to be an error.
func TestLoadRefusesALayerOfOursItDoesNotUnderstand(t *testing.T) {
	s := newLayerStore(t)

	desc := s.image(s.put(MediaTypePrefix+"something.v9", []byte("from the future")))

	img, err := s.load(t.Context(), desc)

	require.Error(t, err, "an unreadable layer of ours must not load as an image with no kernel")
	require.Nil(t, img)
	require.ErrorContains(t, err, "does not understand")
	require.ErrorContains(t, err, "written by something newer")
}

// TestLoadIgnoresALayerThatIsNotOurs covers reading a container image, which is
// done for its configuration and whose layers are none of this package's
// business.
func TestLoadIgnoresALayerThatIsNotOurs(t *testing.T) {
	s := newLayerStore(t)

	desc := s.image(
		s.put(ocispec.MediaTypeImageLayerGzip, []byte("a compressed container layer")),
		s.put("application/vnd.example.something", []byte("somebody else's")),
	)

	img, err := s.load(t.Context(), desc)
	require.NoError(t, err)

	require.Nil(t, img.Kernel, "there was no kernel to find, which is not an error")
	require.NotNil(t, img.Image, "and the configuration was still read")
}

func TestUnikraftMediaTypeRecognisesOurOwn(t *testing.T) {
	for _, mediaType := range []string{MediaTypeKernel, MediaTypeKernelDebug, MediaTypeInitrd, MediaTypeRom, MediaTypePrefix + "not.invented.yet"} {
		require.True(t, unikraftMediaType(mediaType), mediaType)
	}

	for _, mediaType := range []string{ocispec.MediaTypeImageLayer, ocispec.MediaTypeImageLayerGzip, "application/vnd.example.thing"} {
		require.False(t, unikraftMediaType(mediaType), mediaType)
	}
}
