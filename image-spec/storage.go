// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"context"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/distribution/reference"
)

type storageOptions struct {
	remote    remotes.Resolver
	refParser func(string) (reference.Named, error)
}

type StorageOpt func(*storageOptions)

func WithResolver(r remotes.Resolver) StorageOpt {
	return func(so *storageOptions) {
		so.remote = r
	}
}

func WithReferenceParser(rp func(string) (reference.Named, error)) StorageOpt {
	return func(so *storageOptions) {
		so.refParser = rp
	}
}

func Load(ctx context.Context, src string, opts ...StorageOpt) (*Image, error) {
	opt := &storageOptions{
		refParser: reference.ParseNormalizedNamed,
	}
	for _, o := range opts {
		o(opt)
	}

	var stat os.FileInfo
	var statErr error
	if stat, statErr = os.Stat(src); statErr == nil {
		if stat.IsDir() {
			return LoadOCILayoutNamed(ctx, src, "")
		} else {
			return LoadTarball(ctx, src)
		}
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}
	if idx := strings.LastIndex(src, ":"); idx >= 0 {
		path := src[:idx]
		tag := src[idx+1:]
		if stat, statErr = os.Stat(path); statErr == nil {
			if stat.IsDir() {
				return LoadOCILayoutNamed(ctx, path, tag)
			} else {
				return LoadTarball(ctx, path)
			}
		} else if !os.IsNotExist(statErr) {
			return nil, statErr
		}
	}

	if looksLikePath(src) {
		return nil, statErr
	}

	named, err := opt.refParser(src)
	if err != nil {
		return nil, fmt.Errorf("parsing image reference %q: %w", src, err)
	}
	return LoadDockerImage(ctx, named, opt.remote)
}

func Save(ctx context.Context, dest string, img *Image, opts ...StorageOpt) error {
	opt := &storageOptions{
		refParser: reference.ParseNormalizedNamed,
	}
	for _, o := range opts {
		o(opt)
	}

	if stat, statErr := os.Stat(dest); statErr == nil {
		if stat.IsDir() {
			_, err := SaveOCILayoutNamed(ctx, dest, "latest", img)
			return err
		} else {
			return SaveTarball(ctx, dest, img)
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	if idx := strings.LastIndex(dest, ":"); idx >= 0 {
		path := dest[:idx]
		tag := dest[idx+1:]
		if stat, statErr := os.Stat(path); statErr == nil {
			if stat.IsDir() {
				_, err := SaveOCILayoutNamed(ctx, path, tag, img)
				return err
			} else {
				return SaveTarball(ctx, path, img)
			}
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
	}

	if looksLikeTarball(dest) {
		return SaveTarball(ctx, dest, img)
	}
	if looksLikeDir(dest) {
		_, err := SaveOCILayoutNamed(ctx, dest, "latest", img)
		return err
	}
	if idx := strings.LastIndex(dest, ":"); idx >= 0 {
		path := dest[:idx]
		tag := dest[idx+1:]
		if looksLikeTarball(path) {
			return SaveTarball(ctx, path, img)
		}
		if looksLikeDir(path) {
			_, err := SaveOCILayoutNamed(ctx, path, tag, img)
			return err
		}
	}

	if looksLikePath(dest) {
		return fmt.Errorf("ambiguous destination path: %s", dest)
	}

	named, err := opt.refParser(dest)
	if err != nil {
		return fmt.Errorf("parsing image reference %q: %w", dest, err)
	}
	_, _, err = SaveDockerImage(ctx, named, opt.remote, img)
	return err
}

func looksLikePath(s string) bool {
	return strings.HasPrefix(s, ".") || strings.HasPrefix(s, "/")
}

func looksLikeDir(s string) bool {
	return strings.HasSuffix(s, string(os.PathSeparator))
}

func looksLikeTarball(s string) bool {
	return slices.Contains(strings.Split(path.Ext(s), "."), "tar")
}
