// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"

	"github.com/containerd/containerd/v2/core/content"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type File interface {
	Path() string
	Open(ctx context.Context) (io.ReadCloser, int64, error)
	Cleanup() error

	Source() (ocispec.Descriptor, content.Provider)
}

type staticFile struct {
	path string
	data []byte
}

func NewStaticFile(path string, data []byte) File {
	return &staticFile{
		path: path,
		data: data,
	}
}

func (f *staticFile) Path() string {
	return f.path
}

func (f *staticFile) Open(ctx context.Context) (io.ReadCloser, int64, error) {
	return io.NopCloser(bytes.NewReader(f.data)), int64(len(f.data)), nil
}

func (f *staticFile) Cleanup() error {
	return nil
}

func (f *staticFile) Source() (ocispec.Descriptor, content.Provider) {
	return ocispec.Descriptor{}, nil
}

type osFile struct {
	f      *os.File
	delete bool
}

func NewOSFile(f *os.File) File {
	return &osFile{
		f: f,
	}
}

func NewTempOSFile(f *os.File) File {
	return &osFile{
		f:      f,
		delete: true,
	}
}

func (f *osFile) Path() string {
	return ""
}

func (f *osFile) Open(ctx context.Context) (io.ReadCloser, int64, error) {
	fi, err := f.f.Stat()
	if err != nil {
		return nil, 0, err
	}
	newFd, err := os.Open(f.f.Name())
	if err != nil {
		return nil, 0, err
	}
	return newFd, fi.Size(), nil
}

func (f *osFile) Cleanup() error {
	err := f.f.Close()
	if f.delete {
		err = errors.Join(err, os.Remove(f.f.Name()))
	}
	return err
}

func (f *osFile) Source() (ocispec.Descriptor, content.Provider) {
	return ocispec.Descriptor{}, nil
}
