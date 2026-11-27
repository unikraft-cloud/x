// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026, The BuildKit Authors.
// Licensed under the Apache License, Version 2.0 (the "License").
// You may not use this file except in compliance with the License.

// Package ociindex reads and writes the index.json of an OCI image layout.
package ociindex

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"slices"
	"syscall"

	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/errdefs"
	"github.com/gofrs/flock"
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

// StoreIndex is a fork of buildkit's ociindex.StoreIndex, which can delete a
// tag.
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

func (s StoreIndex) read() (*ocispecs.Index, error) {
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

func (s StoreIndex) Get(tag string) (*ocispecs.Descriptor, error) {
	idx, err := s.read()
	if err != nil {
		return nil, err
	}

	for _, m := range idx.Manifests {
		if t, ok := m.Annotations[annotationImageName]; ok && t == tag {
			return &m, nil
		}
	}

	for _, m := range idx.Manifests {
		if t, ok := m.Annotations[ocispecs.AnnotationRefName]; ok && t == tag {
			return &m, nil
		}
	}
	return nil, nil
}

func (s StoreIndex) GetSingle() (*ocispecs.Descriptor, error) {
	idx, err := s.read()
	if err != nil {
		return nil, err
	}

	if len(idx.Manifests) == 1 {
		return &idx.Manifests[0], nil
	}
	return nil, nil
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
