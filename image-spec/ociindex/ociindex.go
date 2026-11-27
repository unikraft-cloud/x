// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026, The BuildKit Authors.
// Licensed under the Apache License, Version 2.0 (the "License").
// You may not use this file except in compliance with the License.

// Package ociindex reads and writes the index.json of an OCI image layout.
package ociindex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"slices"
	"syscall"

	"github.com/containerd/containerd/v2/core/content"
	ctrdimages "github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/errdefs"
	"github.com/gofrs/flock"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	// lockFileSuffix is the suffix of the lock file
	lockFileSuffix = ".lock"

	annotationImageName = "io.containerd.image.name"
)

// ErrTooLarge is returned for an index.json that is larger than the largest
// manifest containerd takes from a registry.
var ErrTooLarge = errors.New("too large")

type tooLargeError struct{ name string }

func (e tooLargeError) Error() string {
	return fmt.Sprintf("%s is larger than %.1f MiB", e.name, float64(docker.MaxManifestSize)/(1<<20))
}

func (tooLargeError) Is(target error) bool { return target == ErrTooLarge }

// ReadIndex reads the index.json called name from r, up to the size of the
// largest manifest containerd takes from a registry.
func ReadIndex(r io.Reader, name string) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, docker.MaxManifestSize+1))
	if err != nil {
		return nil, fmt.Errorf("could not read %s: %w", name, err)
	}
	if int64(len(b)) > docker.MaxManifestSize {
		return nil, tooLargeError{name}
	}
	return b, nil
}

// StoreIndex is a fork of buildkit's ociindex.StoreIndex, which picks an image
// by Select, and can delete a tag.
// Source: github.com/moby/buildkit/client/ociindex/ociindex.go
type StoreIndex struct {
	indexPath  string
	lockPath   string
	layoutPath string
}

func NewStoreIndex(storePath string) StoreIndex {
	indexPath := path.Join(storePath, ocispecs.ImageIndexFile)
	layoutPath := path.Join(storePath, ocispecs.ImageLayoutFile)
	return StoreIndex{
		indexPath:  indexPath,
		lockPath:   indexPath + lockFileSuffix,
		layoutPath: layoutPath,
	}
}

// Read returns the index.json as it is stored.
func (s StoreIndex) Read() ([]byte, error) {
	lock := flock.New(s.lockPath)
	locked, err := lock.TryRLock()
	if err != nil {
		if !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.EROFS) {
			return nil, fmt.Errorf("could not lock %s: %w", s.lockPath, err)
		}
	} else {
		if !locked {
			return nil, fmt.Errorf("could not lock %s", s.lockPath)
		}
		defer func() {
			_ = lock.Unlock()
			os.RemoveAll(s.lockPath)
		}()
	}

	f, err := os.Open(s.indexPath)
	if err != nil {
		return nil, fmt.Errorf("could not read %s: %w", s.indexPath, err)
	}
	defer f.Close()
	b, err := ReadIndex(f, s.indexPath)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func (s StoreIndex) read() (*ocispecs.Index, error) {
	b, err := s.Read()
	if err != nil {
		return nil, err
	}
	var idx ocispecs.Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("could not unmarshal %s: %w", s.indexPath, err)
	}
	return &idx, nil
}

// Put lists desc in the index under tag, in place of what the tag named.
func (s StoreIndex) Put(desc ocispecs.Descriptor, tag string) error {
	return s.update(true, func(idx *ocispecs.Index) error {
		// create the oci-layout file
		layout := ocispecs.ImageLayout{
			Version: ocispecs.ImageLayoutVersion,
		}
		layoutData, err := json.Marshal(layout)
		if err != nil {
			return err
		}
		if err := os.WriteFile(s.layoutPath, layoutData, 0o644); err != nil {
			return err
		}

		insertDesc(idx, desc, tag)
		return nil
	})
}

// Delete removes the entries that tag names from the index.
func (s StoreIndex) Delete(tag string) error {
	if tag == "" {
		return errors.New("no tag to delete")
	}
	return s.update(false, func(idx *ocispecs.Index) error {
		entries := len(idx.Manifests)
		idx.Manifests = slices.DeleteFunc(idx.Manifests, func(m ocispecs.Descriptor) bool {
			return named(m, tag)
		})
		if len(idx.Manifests) == entries {
			return fmt.Errorf("tag %q: %w", tag, errdefs.ErrNotFound)
		}
		return nil
	})
}

// named reports whether tag is the ref name of the entry desc.
func named(desc ocispecs.Descriptor, tag string) bool {
	name, ok := desc.Annotations[ocispecs.AnnotationRefName]
	return ok && name == tag
}

// update rewrites the index as fn changes it, under the lock of the store.
func (s StoreIndex) update(create bool, fn func(*ocispecs.Index) error) error {
	// lock the store to prevent concurrent access
	lock := flock.New(s.lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		return fmt.Errorf("could not lock %s: %w", s.lockPath, err)
	}
	if !locked {
		return fmt.Errorf("could not lock %s", s.lockPath)
	}
	defer func() {
		_ = lock.Unlock()
		os.RemoveAll(s.lockPath)
	}()

	// modify the index file
	flag := os.O_RDWR
	if create {
		flag |= os.O_CREATE
	}
	idxFile, err := os.OpenFile(s.indexPath, flag, 0o644)
	if err != nil {
		return fmt.Errorf("could not open %s: %w", s.indexPath, err)
	}
	defer idxFile.Close()

	var idx ocispecs.Index
	idxData, err := ReadIndex(idxFile, s.indexPath)
	if err != nil {
		return err
	}
	if len(idxData) > 0 {
		if err := json.Unmarshal(idxData, &idx); err != nil {
			return fmt.Errorf("could not unmarshal %s: %w", s.indexPath, err)
		}
	}

	setOCIIndexDefaults(&idx)

	if err := fn(&idx); err != nil {
		return err
	}

	idxData, err = json.Marshal(idx)
	if err != nil {
		return err
	}
	if _, err = idxFile.WriteAt(idxData, 0); err != nil {
		return fmt.Errorf("could not write %s: %w", s.indexPath, err)
	}
	if err = idxFile.Truncate(int64(len(idxData))); err != nil {
		return fmt.Errorf("could not truncate %s: %w", s.indexPath, err)
	}
	return nil
}

// Get returns the image that tag names in the index, as Select picks it.
func (s StoreIndex) Get(tag string) (ocispecs.Descriptor, error) {
	b, err := s.Read()
	if err != nil {
		return ocispecs.Descriptor{}, err
	}
	var idx ocispecs.Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return ocispecs.Descriptor{}, fmt.Errorf("could not unmarshal %s: %w", s.indexPath, err)
	}
	return Select(Descriptor(b), idx.Manifests, tag)
}

// Descriptor describes an index.json, which is an image index.
func Descriptor(index []byte) ocispecs.Descriptor {
	return ocispecs.Descriptor{
		MediaType: ocispecs.MediaTypeImageIndex,
		Digest:    digest.FromBytes(index),
		Size:      int64(len(index)),
	}
}

// WithIndex returns provider with index, an index.json, readable by the digest
// of its Descriptor: it can be the root of an image, but is not a blob of its
// layout.
func WithIndex(provider content.Provider, index []byte) content.Provider {
	return indexProvider{Provider: provider, desc: Descriptor(index), index: index}
}

type indexProvider struct {
	content.Provider
	desc  ocispecs.Descriptor
	index []byte
}

func (p indexProvider) ReaderAt(ctx context.Context, desc ocispecs.Descriptor) (content.ReaderAt, error) {
	if desc.Digest == p.desc.Digest {
		return indexReaderAt{bytes.NewReader(p.index)}, nil
	}
	return p.Provider.ReaderAt(ctx, desc)
}

type indexReaderAt struct{ *bytes.Reader }

func (indexReaderAt) Close() error { return nil }

// maxEntries matches the maxReferences of containerd, which is how many
// descriptors it takes in one walk of an image.
const maxEntries = 10000

// Select picks the image that tag names among the entries of the index.json
// that index describes. Without a tag, that is the only image with a name, or
// else the one named latest. If no entry has a name, it is the only image, or
// else index itself, when the entries are the manifests of one image. The
// descriptor is the image's, and carries none of the names of its entry.
// NOTE: a tag has to match even for a lone image, unlike the agent, which takes the only entry.
func Select(index ocispecs.Descriptor, entries []ocispecs.Descriptor, tag string) (ocispecs.Descriptor, error) {
	if len(entries) > maxEntries {
		return ocispecs.Descriptor{}, fmt.Errorf("index lists too many descriptors: %d, more than %d", len(entries), maxEntries)
	}
	root, err := selectEntry(index, entries, tag)
	if err != nil {
		return ocispecs.Descriptor{}, err
	}
	root.Annotations = maps.Clone(root.Annotations)
	delete(root.Annotations, annotationImageName)
	delete(root.Annotations, ocispecs.AnnotationRefName)
	if len(root.Annotations) == 0 {
		root.Annotations = nil
	}
	return root, nil
}

func selectEntry(index ocispecs.Descriptor, entries []ocispecs.Descriptor, tag string) (ocispecs.Descriptor, error) {
	if tag != "" {
		entry, ok, err := taggedEntry(entries, tag)
		if err == nil && !ok {
			err = fmt.Errorf("tag %q: %w", tag, errdefs.ErrNotFound)
		}
		return entry, err
	}

	// Next to an entry with a name, one without is a part of it, as its platforms and referrers are.
	names := slices.DeleteFunc(slices.Clone(entries), func(d ocispecs.Descriptor) bool {
		return d.Annotations[ocispecs.AnnotationRefName] == ""
	})
	if len(names) == 0 {
		images := distinct(entries)
		switch {
		case len(images) == 0:
			return ocispecs.Descriptor{}, fmt.Errorf("index lists no images: %w", errdefs.ErrNotFound)
		case len(images) == 1:
			return images[0], nil
		case index.Digest != "" && !slices.ContainsFunc(images, func(d ocispecs.Descriptor) bool { return !ctrdimages.IsManifestType(d.MediaType) }):
			return index, nil
		}
		return ocispecs.Descriptor{}, fmt.Errorf("index lists %d images with no name: give a tag", len(images))
	}

	// docker save lists an image once per tag.
	images := distinct(names)
	if len(images) == 1 {
		return images[0], nil
	}
	if entry, ok, err := taggedEntry(names, "latest"); ok || err != nil {
		return entry, err
	}
	return ocispecs.Descriptor{}, fmt.Errorf("index lists %d images, and none is tagged latest: give a tag", len(images))
}

// distinct returns the first entry of each digest.
func distinct(entries []ocispecs.Descriptor) []ocispecs.Descriptor {
	seen := make(map[digest.Digest]struct{}, len(entries))
	var images []ocispecs.Descriptor
	for _, entry := range entries {
		if _, ok := seen[entry.Digest]; !ok {
			seen[entry.Digest] = struct{}{}
			images = append(images, entry)
		}
	}
	return images
}

// taggedEntry returns the entry that tag names. It does not pick between two
// images that share the tag.
func taggedEntry(entries []ocispecs.Descriptor, tag string) (ocispecs.Descriptor, bool, error) {
	var match *ocispecs.Descriptor
	for _, entry := range entries {
		switch {
		case !named(entry, tag):
		case match == nil:
			match = &entry
		case match.Digest != entry.Digest:
			return ocispecs.Descriptor{}, false, fmt.Errorf("tag %q names several images", tag)
		}
	}
	if match == nil {
		return ocispecs.Descriptor{}, false, nil
	}
	return *match, true, nil
}

// setOCIIndexDefaults updates zero values in index to their default values.
func setOCIIndexDefaults(index *ocispecs.Index) {
	if index == nil {
		return
	}
	if index.SchemaVersion == 0 {
		index.SchemaVersion = 2
	}
	if index.MediaType == "" {
		index.MediaType = ocispecs.MediaTypeImageIndex
	}
}

// insertDesc puts desc to index with tag.
// Existing manifests that the tag names will be removed from the index.
func insertDesc(index *ocispecs.Index, in ocispecs.Descriptor, tag string) {
	// make a copy to not modify the input descriptor
	desc := in
	desc.Annotations = maps.Clone(in.Annotations)
	if desc.Annotations == nil {
		desc.Annotations = make(map[string]string)
	}
	desc.Annotations[ocispecs.AnnotationRefName] = tag

	// remove existing manifests with the same tag
	index.Manifests = slices.DeleteFunc(index.Manifests, func(m ocispecs.Descriptor) bool {
		return named(m, tag)
	})
	index.Manifests = append(index.Manifests, desc)
}
