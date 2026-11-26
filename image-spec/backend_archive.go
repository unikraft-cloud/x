// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build !js

package imagespec

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images/archive"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"unikraft.com/x/image-spec/ociindex"
	"unikraft.com/x/image-spec/reference"
)

// This files provides functions for loading and saving images to/from tarball files using
// containerd's archive package.
//
// This is convenient for moving images around, as well as an intermediate
// format for building images before pushing them.

// LoadTarball loads an image from a tarball (OCI or Docker format), from desc
// if it is set, or else as LoadTarballNamed does with no tag.
func LoadTarball(ctx context.Context, tarballPath string, desc ocispec.Descriptor, platform platforms.MatchComparer) (*Image, error) {
	return loadTarballFile(ctx, tarballPath, "", desc, platform)
}

// LoadTarballNamed loads the image that tag names in the index.json of a
// tarball (OCI or Docker format), as for a layout. Without a tag, that is its
// only image, or else the one tagged latest.
func LoadTarballNamed(ctx context.Context, tarballPath string, tag string, platform platforms.MatchComparer) (*Image, error) {
	return loadTarballFile(ctx, tarballPath, tag, ocispec.Descriptor{}, platform)
}

// LoadAllTarballs loads the images of every platform from a tarball (OCI or
// Docker format), under the one root that LoadTarball loads from, and not
// every image the tarball lists. The images share one temporary store, which
// stays until all of them are closed.
func LoadAllTarballs(ctx context.Context, tarballPath string, desc ocispec.Descriptor, platform platforms.MatchComparer) ([]*Image, error) {
	return loadAllTarballsFile(ctx, tarballPath, "", desc, platform)
}

// LoadAllTarballsNamed loads the images of every platform from a tarball (OCI
// or Docker format), under the one root that LoadTarballNamed loads from. The
// images share one temporary store, which stays until all of them are closed.
func LoadAllTarballsNamed(ctx context.Context, tarballPath string, tag string, platform platforms.MatchComparer) ([]*Image, error) {
	return loadAllTarballsFile(ctx, tarballPath, tag, ocispec.Descriptor{}, platform)
}

// LoadTarballReader loads an image from a tarball reader (OCI or Docker
// format), as LoadTarball does.
func LoadTarballReader(ctx context.Context, r io.Reader, desc ocispec.Descriptor, platform platforms.MatchComparer) (*Image, error) {
	return loadTarball(ctx, r, "", desc, platform)
}

// LoadTarballReaderNamed loads an image from a tarball reader (OCI or Docker
// format), as LoadTarballNamed does.
func LoadTarballReaderNamed(ctx context.Context, r io.Reader, tag string, platform platforms.MatchComparer) (*Image, error) {
	return loadTarball(ctx, r, tag, ocispec.Descriptor{}, platform)
}

// LoadAllTarballReaders loads the images of every platform from a tarball
// reader (OCI or Docker format), as LoadAllTarballs does.
func LoadAllTarballReaders(ctx context.Context, r io.Reader, desc ocispec.Descriptor, platform platforms.MatchComparer) ([]*Image, error) {
	return loadAllTarballs(ctx, r, "", desc, platform)
}

// LoadAllTarballReadersNamed loads the images of every platform from a tarball
// reader (OCI or Docker format), as LoadAllTarballsNamed does.
func LoadAllTarballReadersNamed(ctx context.Context, r io.Reader, tag string, platform platforms.MatchComparer) ([]*Image, error) {
	return loadAllTarballs(ctx, r, tag, ocispec.Descriptor{}, platform)
}

func loadTarballFile(ctx context.Context, tarballPath string, tag string, desc ocispec.Descriptor, platform platforms.MatchComparer) (*Image, error) {
	f, err := os.Open(tarballPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open tarball: %w", err)
	}
	defer f.Close()

	img, err := loadTarball(ctx, f, tag, desc, platform)
	if err != nil {
		return nil, fmt.Errorf("tarball %q: %w", tarballPath, err)
	}
	return img, nil
}

func loadAllTarballsFile(ctx context.Context, tarballPath string, tag string, desc ocispec.Descriptor, platform platforms.MatchComparer) ([]*Image, error) {
	f, err := os.Open(tarballPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open tarball: %w", err)
	}
	defer f.Close()

	imgs, err := loadAllTarballs(ctx, f, tag, desc, platform)
	if err != nil {
		return nil, fmt.Errorf("tarball %q: %w", tarballPath, err)
	}
	return imgs, nil
}

func loadTarball(ctx context.Context, r io.Reader, tag string, desc ocispec.Descriptor, platform platforms.MatchComparer) (*Image, error) {
	store, root, cleanup, err := importTarball(ctx, r, tag, desc)
	if err != nil {
		return nil, err
	}

	img, err := LoadContent(ctx, store, root, platform)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("failed to load image from imported tarball: %w", err), cleanup())
	}
	img.cleanup = append(img.cleanup, cleanup)
	return img, nil
}

func loadAllTarballs(ctx context.Context, r io.Reader, tag string, desc ocispec.Descriptor, platform platforms.MatchComparer) ([]*Image, error) {
	store, root, cleanup, err := importTarball(ctx, r, tag, desc)
	if err != nil {
		return nil, err
	}

	imgs, err := LoadAllContent(ctx, store, root, platform)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("failed to load images from imported tarball: %w", err), cleanup())
	}
	if len(imgs) == 0 {
		return imgs, cleanup()
	}
	for i, release := range sharedCleanup(cleanup, len(imgs)) {
		imgs[i].cleanup = append(imgs[i].cleanup, release)
	}
	return imgs, nil
}

// sharedCleanup returns n functions that each release one user of cleanup, which runs when the last one is released.
func sharedCleanup(cleanup func() error, n int) []func() error {
	var left atomic.Int64
	left.Store(int64(n))
	releases := make([]func() error, n)
	for i := range releases {
		releases[i] = sync.OnceValue(func() error {
			if left.Add(-1) == 0 {
				return cleanup()
			}
			return nil
		})
	}
	return releases
}

// importTarball imports the tarball r into a temporary store, which cleanup
// removes, and returns the root to load from: desc if it is set, or else what
// tag names in the tarball's index.json.
func importTarball(ctx context.Context, r io.Reader, tag string, desc ocispec.Descriptor) (_ content.Store, _ ocispec.Descriptor, _ func() error, rerr error) {
	store, index, entries, cleanup, err := importTarballIndex(ctx, r)
	if err != nil {
		return nil, ocispec.Descriptor{}, nil, err
	}
	defer func() {
		if rerr != nil {
			_ = cleanup()
		}
	}()

	if desc.Digest != "" {
		if _, err := store.Info(ctx, desc.Digest); err != nil {
			return nil, ocispec.Descriptor{}, nil, fmt.Errorf("tarball does not contain %s: %w", desc.Digest, err)
		}
		return store, desc, cleanup, nil
	}
	root, err := ociindex.Select(index, entries, tag)
	if err != nil {
		return nil, ocispec.Descriptor{}, nil, err
	}
	return store, root, cleanup, nil
}

// importTarballIndex imports the tarball r into a temporary store, which
// cleanup removes, and returns its index.json and the entries of that.
func importTarballIndex(ctx context.Context, r io.Reader) (_ content.Store, _ ocispec.Descriptor, _ []ocispec.Descriptor, _ func() error, rerr error) {
	tmpDir, err := os.MkdirTemp("", "imagespec-tarball-*")
	if err != nil {
		return nil, ocispec.Descriptor{}, nil, nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	cleanup := func() error {
		return os.RemoveAll(tmpDir)
	}
	defer func() {
		if rerr != nil {
			_ = cleanup()
		}
	}()

	store, err := local.NewStore(tmpDir)
	if err != nil {
		return nil, ocispec.Descriptor{}, nil, nil, fmt.Errorf("failed to create local store: %w", err)
	}

	desc, err := archive.ImportIndex(ctx, store, r)
	if err != nil {
		return nil, ocispec.Descriptor{}, nil, nil, fmt.Errorf("failed to import tarball: %w", err)
	}
	ra, err := store.ReaderAt(ctx, desc)
	if err != nil {
		return nil, ocispec.Descriptor{}, nil, nil, fmt.Errorf("failed to read index of tarball: %w", err)
	}
	defer ra.Close()
	data, err := ociindex.ReadIndex(content.NewReader(ra), "index.json of tarball")
	if err != nil {
		return nil, ocispec.Descriptor{}, nil, nil, err
	}
	if desc.Digest.Algorithm().FromBytes(data) != desc.Digest {
		return nil, ocispec.Descriptor{}, nil, nil, fmt.Errorf("failed to read index of tarball: %w: blob %s", ErrDigestMismatch, desc.Digest)
	}
	var index ocispec.Index
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, ocispec.Descriptor{}, nil, nil, fmt.Errorf("failed to unmarshal index of tarball: %w", err)
	}
	return store, desc, index.Manifests, cleanup, nil
}

// tarballIndex returns the index.json of the tarball at tarballPath and its
// entries, as loading it would see them. It only reads that file and the
// oci-layout one, unless they do not make a layout: then the tarball is
// imported, as one in the Docker format has to be.
func tarballIndex(ctx context.Context, tarballPath string) (ocispec.Descriptor, []ocispec.Descriptor, error) {
	f, err := os.Open(tarballPath)
	if err != nil {
		return ocispec.Descriptor{}, nil, fmt.Errorf("failed to open tarball: %w", err)
	}
	defer f.Close()

	data, ok, err := scanTarballIndex(f)
	if err != nil {
		return ocispec.Descriptor{}, nil, err
	}
	if ok {
		var index ocispec.Index
		if err := json.Unmarshal(data, &index); err == nil {
			return ociindex.Descriptor(data), index.Manifests, nil
		}
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ocispec.Descriptor{}, nil, err
	}
	_, index, entries, cleanup, err := importTarballIndex(ctx, f)
	if err != nil {
		return ocispec.Descriptor{}, nil, err
	}
	return index, entries, cleanup()
}

// maxTarballLayoutSize bounds the oci-layout file that is read out of a
// tarball, which only holds a version.
const maxTarballLayoutSize = 4 << 10

// scanTarballIndex reads the index.json of a layout tarball from its headers.
// The last of a name counts, as it does when the tarball is imported. It fails
// for an index.json that is too large, which importing fails for as well.
func scanTarballIndex(r io.Reader) ([]byte, bool, error) {
	var index []byte
	var layout ocispec.ImageLayout
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, false, nil
		}
		switch path.Clean(hdr.Name) {
		case ocispec.ImageIndexFile:
			index, err = ociindex.ReadIndex(tr, "index.json of tarball")
			if errors.Is(err, ociindex.ErrTooLarge) {
				return nil, false, err
			}
			if err != nil {
				return nil, false, nil
			}
		case ocispec.ImageLayoutFile:
			data, err := io.ReadAll(io.LimitReader(tr, maxTarballLayoutSize+1))
			if err != nil || len(data) > maxTarballLayoutSize || json.Unmarshal(data, &layout) != nil {
				return nil, false, nil
			}
		}
	}
	return index, index != nil && layout.Version == ocispec.ImageLayoutVersion, nil
}

// tarballHoldsOthers reports whether a tarball with these entries lists an
// image under another name than tag, which rewriting or removing it for tag
// would lose.
func tarballHoldsOthers(entries []ocispec.Descriptor, tag string) bool {
	return slices.ContainsFunc(entries, func(d ocispec.Descriptor) bool {
		name := d.Annotations[ocispec.AnnotationRefName]
		return name != "" && name != tag
	})
}

// DeleteTarballNamed deletes a tarball in which tag names an image, as it
// would for LoadTarballNamed. A tarball is not rewritten to drop one image of
// several, so one that lists images under other tags is left alone, with an
// error.
func DeleteTarballNamed(ctx context.Context, tarballPath string, tag string) error {
	index, entries, err := tarballIndex(ctx, tarballPath)
	if err != nil {
		return fmt.Errorf("tarball %q: %w", tarballPath, err)
	}
	if _, err := ociindex.Select(index, entries, tag); err != nil {
		return fmt.Errorf("tarball %q: %w", tarballPath, err)
	}
	if tarballHoldsOthers(entries, tag) {
		return fmt.Errorf("tarball %q holds other images than %q, and can only be deleted whole", tarballPath, tag)
	}
	return os.Remove(tarballPath)
}

// SaveTarball saves an image to a tarball file, as SaveTarballWriter does, in
// place of the file that was there.
func SaveTarball(ctx context.Context, tarballPath string, image ...*Image) (ocispec.Descriptor, error) {
	return replaceTarball(tarballPath, func(w io.Writer) (ocispec.Descriptor, error) {
		return SaveTarballWriter(ctx, w, image...)
	})
}

// SaveTarballNamed saves an image to a tarball file, as SaveTarballWriterNamed
// does. The file is written anew, so it is refused if the tarball that is
// there lists images under other tags, which would be dropped.
func SaveTarballNamed(ctx context.Context, tarballPath string, tag string, image ...*Image) (ocispec.Descriptor, error) {
	if err := checkTarballTag(ctx, tarballPath, tag); err != nil {
		return ocispec.Descriptor{}, err
	}
	return replaceTarball(tarballPath, func(w io.Writer) (ocispec.Descriptor, error) {
		return SaveTarballWriterNamed(ctx, w, tag, image...)
	})
}

// tarballSaveTag splits the path of an archive to save to from its tag, which
// is latest if it has none. A tag that was written is refused if the tarball
// that is there lists images under other tags, which the save would drop.
func tarballSaveTag(ctx context.Context, archivePath string) (string, string, error) {
	tarballPath, tag := splitPathTag(archivePath)
	if tag == "" {
		return tarballPath, "latest", nil
	}
	return tarballPath, tag, checkTarballTag(ctx, tarballPath, tag)
}

func checkTarballTag(ctx context.Context, tarballPath string, tag string) error {
	_, entries, err := tarballIndex(ctx, tarballPath)
	switch {
	case errors.Is(err, ociindex.ErrTooLarge):
		return fmt.Errorf("tarball %q: %w", tarballPath, err)
	case err != nil:
		// A file that cannot be read as a tarball has nothing to drop.
		return nil
	case tarballHoldsOthers(entries, tag):
		return fmt.Errorf("tarball %q holds other images than %q, and is not rewritten to add one", tarballPath, tag)
	}
	return nil
}

// replaceTarball writes a tarball to tarballPath with write. Over a tarball
// that is there, it writes to a temporary file beside it first, and moves that
// into place once it is whole, so that a failed save leaves what was there.
func replaceTarball(tarballPath string, write func(io.Writer) (ocispec.Descriptor, error)) (ocispec.Descriptor, error) {
	// A symlink is saved through, and not replaced.
	target, err := filepath.EvalSymlinks(tarballPath)
	if err != nil {
		target = tarballPath
	}
	old, err := os.Stat(target)
	if err != nil || !old.Mode().IsRegular() {
		return writeTarball(target, err != nil, write)
	}
	f, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".*")
	if err != nil {
		// Without a file beside it, as in a directory that cannot be written, save over it.
		return writeTarball(target, false, write)
	}
	keep := false
	defer func() {
		_ = f.Close()
		if !keep {
			_ = os.Remove(f.Name())
		}
	}()

	desc, err := write(f)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	if err := f.Chmod(old.Mode().Perm()); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to create tarball file: %w", err)
	}
	if err := f.Close(); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to write tarball file: %w", err)
	}
	if err := os.Rename(f.Name(), target); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to create tarball file: %w", err)
	}
	keep = true
	return desc, nil
}

// writeTarball writes straight to tarballPath. A file that it made is removed
// if the save fails.
func writeTarball(tarballPath string, made bool, write func(io.Writer) (ocispec.Descriptor, error)) (ocispec.Descriptor, error) {
	f, err := os.Create(tarballPath)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to create tarball file: %w", err)
	}
	defer f.Close()
	// A dangling symlink is saved through: the file made is its target.
	if made {
		if p, err := filepath.EvalSymlinks(tarballPath); err == nil {
			tarballPath = p
		}
	}

	desc, err := write(f)
	if err != nil && made {
		_ = f.Close()
		_ = os.Remove(tarballPath)
	}
	return desc, err
}

// SaveTarballWriter saves an image to a tarball writer under one index, and
// returns the descriptor of that index. The tarball's index.json lists it
// with no name.
func SaveTarballWriter(ctx context.Context, w io.Writer, image ...*Image) (ocispec.Descriptor, error) {
	return SaveTarballWriterNamed(ctx, w, "", image...)
}

// SaveTarballWriterNamed saves an image to a tarball writer under one index,
// and returns the descriptor of that index. The tarball's index.json lists it
// under tag, as the index.json of a layout does.
func SaveTarballWriterNamed(ctx context.Context, w io.Writer, tag string, image ...*Image) (ocispec.Descriptor, error) {
	tmpDir, err := os.MkdirTemp("", "imagespec-tarball-*")
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := local.NewStore(tmpDir)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to create local store: %w", err)
	}

	desc, err := SaveContent(ctx, store, "latest", image...)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to save image to local store: %w", err)
	}

	// Export requires content.InfoReaderProvider which local.Store implements
	infoStore, ok := store.(content.InfoReaderProvider)
	if !ok {
		return ocispec.Descriptor{}, fmt.Errorf("local store does not implement content.InfoReaderProvider")
	}

	if err := exportTarball(ctx, w, infoStore, desc, tag); err != nil {
		return ocispec.Descriptor{}, err
	}
	return desc, nil
}

// copyToTarball copies root from provider to w as a tarball, as copyContent
// does, and returns the root it wrote. The tarball's index.json lists it under
// tag.
func copyToTarball(ctx context.Context, w io.Writer, tag string, provider content.Provider, source reference.Reference, root ocispec.Descriptor, platform platforms.MatchComparer) (ocispec.Descriptor, error) {
	tmpDir, err := os.MkdirTemp("", "imagespec-tarball-*")
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := local.NewStore(tmpDir)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to create local store: %w", err)
	}
	ingester := ingestRef(store, "latest")
	root, err = copyContent(ctx, provider, ingester, ingester, source, root, platform)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to copy image to local store: %w", err)
	}
	if err := exportTarball(ctx, w, store, root, tag, archive.WithSkipNonDistributableBlobs()); err != nil {
		return ocispec.Descriptor{}, err
	}
	return root, nil
}

// exportTarball writes the tarball of the image at root in store, whose
// index.json lists it under tag, if there is one.
func exportTarball(ctx context.Context, w io.Writer, store content.InfoReaderProvider, root ocispec.Descriptor, tag string, opts ...archive.ExportOpt) error {
	// NOTE: a name given to WithManifest would also be written as the image name, which a tag is not.
	entry := root
	if tag != "" {
		entry.Annotations = map[string]string{ocispec.AnnotationRefName: tag}
	}
	if err := archive.Export(ctx, store, w, append(opts, archive.WithManifest(entry))...); err != nil {
		return fmt.Errorf("failed to export tarball: %w", err)
	}
	return nil
}
