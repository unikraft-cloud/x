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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/image-spec/ociindex"
	"unikraft.com/x/image-spec/schemes"
)

func platformImages(ps ...ocispec.Platform) []*Image {
	imgs := make([]*Image, 0, len(ps))
	for _, p := range ps {
		img := *testImage
		config := *img.Image
		config.Platform = p
		img.Image = &config
		imgs = append(imgs, &img)
	}
	return imgs
}

func TestSaveLoadRoundTrip(t *testing.T) {
	fcIntel := ocispec.Platform{OS: "fc", Architecture: "x86_64"}
	fcArm := ocispec.Platform{OS: "fc", Architecture: "arm64"}
	qemuIntel := ocispec.Platform{OS: "qemu", Architecture: "x86_64"}

	accessor := NewAccessor()

	for _, tc := range []struct {
		name string
		dest func(t *testing.T) Location
		// wantGone is the error loading the first save gives once the
		// destination has been overwritten, or empty if it still loads.
		wantGone string
	}{
		{
			name: "layout",
			dest: func(t *testing.T) Location { return Location{Scheme: schemes.OCILayout, Path: t.TempDir() + ":v1"} },
		},
		{
			name: "archive",
			dest: func(t *testing.T) Location {
				return Location{Scheme: schemes.OCIArchive, Path: filepath.Join(t.TempDir(), "app.tar")}
			},
			wantGone: "tarball does not contain",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			dest := tc.dest(t)

			saved, err := accessor.Save(ctx, &dest, platformImages(fcIntel, fcArm)...)
			require.NoError(t, err)
			require.NotEmpty(t, saved.Descriptor.Digest)

			imgs, err := accessor.LoadAll(ctx, saved, platforms.All)
			require.NoError(t, err)
			require.Len(t, imgs, 2)
			for _, img := range imgs {
				assert.Equal(t, saved, img.Resolved)
				assert.NotEqual(t, saved.Descriptor, img.ManifestDescriptor)
				require.NoError(t, img.Close())
			}

			img, err := accessor.Load(ctx, saved, platforms.Only(fcArm))
			require.NoError(t, err)
			assert.Equal(t, fcArm, img.Image.Platform)
			require.NoError(t, img.Close())

			_, err = accessor.Save(ctx, &dest, platformImages(qemuIntel)...)
			require.NoError(t, err)

			imgs, err = accessor.LoadAll(ctx, saved, platforms.All)
			if tc.wantGone != "" {
				require.ErrorContains(t, err, tc.wantGone)
				return
			}
			require.NoError(t, err)
			require.Len(t, imgs, 2, "the first save is still what saved names")
			for _, img := range imgs {
				require.NoError(t, img.Close())
			}
		})
	}
}

func TestLoadUnpinned(t *testing.T) {
	ctx := t.Context()
	fcIntel := ocispec.Platform{OS: "fc", Architecture: "x86_64"}
	qemuIntel := ocispec.Platform{OS: "qemu", Architecture: "x86_64"}

	accessor := NewAccessor()

	t.Run("layout", func(t *testing.T) {
		dest := Location{Scheme: schemes.OCILayout, Path: t.TempDir() + ":v1"}
		_, err := accessor.Save(ctx, &dest, platformImages(fcIntel)...)
		require.NoError(t, err)
		_, err = accessor.Save(ctx, &dest, platformImages(qemuIntel)...)
		require.NoError(t, err)

		img, err := accessor.Load(ctx, Resolved{Location: dest}, platforms.All)
		require.NoError(t, err)
		assert.Equal(t, qemuIntel, img.Image.Platform, "an unpinned layout follows its tag")
		require.NoError(t, img.Close())

		path, _ := splitPathTag(dest.Path)
		data, err := os.ReadFile(filepath.Join(path, ocispec.ImageIndexFile))
		require.NoError(t, err)
		var index ocispec.Index
		require.NoError(t, json.Unmarshal(data, &index))
		require.Len(t, index.Manifests, 1)
		assert.Equal(t, index.Manifests[0].Digest, img.Descriptor.Digest, "an unpinned layout is pinned to what its tag names")
	})

	t.Run("archive", func(t *testing.T) {
		dest := Location{Scheme: schemes.OCIArchive, Path: filepath.Join(t.TempDir(), "app.tar")}
		saved, err := accessor.Save(ctx, &dest, platformImages(fcIntel)...)
		require.NoError(t, err)

		img, err := accessor.Load(ctx, Resolved{Location: dest}, platforms.All)
		require.NoError(t, err)
		require.NoError(t, img.Close())
		assert.Equal(t, saved, img.Resolved)

		f, err := os.Open(dest.Path)
		require.NoError(t, err)
		defer f.Close()
		_, r, err := readTarFile(tar.NewReader(f), "index.json")
		require.NoError(t, err)
		data, err := io.ReadAll(r)
		require.NoError(t, err)
		var index ocispec.Index
		require.NoError(t, json.Unmarshal(data, &index))
		require.Len(t, index.Manifests, 1)
		assert.Equal(t, index.Manifests[0].Digest, img.Descriptor.Digest, "an unpinned archive is pinned to what its index.json names")
		assert.Equal(t, map[string]string{ocispec.AnnotationRefName: "latest"}, index.Manifests[0].Annotations)

		wrapper := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageIndex, Digest: digest.FromBytes(data), Size: int64(len(data))}
		old, err := accessor.Load(ctx, Resolved{Location: dest, Descriptor: wrapper}, platforms.All)
		require.NoError(t, err, "an archive pinned to its index.json, as it was before, still loads")
		assert.Equal(t, img.ManifestDescriptor, old.ManifestDescriptor)
		require.NoError(t, old.Close())

		again, err := accessor.Load(ctx, img.Resolved, platforms.All)
		require.NoError(t, err)
		assert.Equal(t, img.Resolved, again.Resolved)
		require.NoError(t, again.Close())
	})
}

func tarLayout(t *testing.T, layout string) []byte {
	t.Helper()
	var tarball bytes.Buffer
	tw := tar.NewWriter(&tarball)
	require.NoError(t, tw.AddFS(os.DirFS(layout)))
	require.NoError(t, tw.Close())
	return tarball.Bytes()
}

func TestLoadSameRoot(t *testing.T) {
	ctx := t.Context()
	accessor := NewAccessor()

	layout := t.TempDir()
	v1, err := accessor.Save(ctx, &Location{Scheme: schemes.OCILayout, Path: layout + ":v1"},
		platformImages(ocispec.Platform{OS: "fc", Architecture: "x86_64"})...)
	require.NoError(t, err)
	latest, err := accessor.Save(ctx, &Location{Scheme: schemes.OCILayout, Path: layout},
		platformImages(ocispec.Platform{OS: "qemu", Architecture: "x86_64"})...)
	require.NoError(t, err)
	require.NotEqual(t, v1.Descriptor, latest.Descriptor)

	tarball := tarLayout(t, layout)
	tarPath := filepath.Join(t.TempDir(), "layout.tar")
	require.NoError(t, os.WriteFile(tarPath, tarball, 0o644))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ns/redirect/") {
			http.Redirect(w, r, "/ns/direct/@"+digest.FromBytes(tarball).String(), http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", MediaTypeOCILayoutTar)
		_, _ = w.Write(tarball)
	}))
	t.Cleanup(srv.Close)

	local := func(scheme schemes.Scheme, path string) func(string) Location {
		return func(tag string) Location {
			if tag != "" {
				return Location{Scheme: scheme, Path: path + ":" + tag}
			}
			return Location{Scheme: scheme, Path: path}
		}
	}
	// A URL carries no tag, whatever its last segment looks like.
	remote := func(path string) func(string) Location {
		return func(string) Location {
			return Location{Scheme: schemes.HTTPOCI, Path: srv.Listener.Addr().String() + path}
		}
	}
	sources := []struct {
		name   string
		at     func(tag string) Location
		tagged bool
	}{
		{name: "layout", at: local(schemes.OCILayout, layout), tagged: true},
		{name: "archive", at: local(schemes.OCIArchive, tarPath), tagged: true},
		{name: "http", at: remote("/ns/direct/v1")},
		{name: "http-redirect", at: remote("/ns/redirect/v1")},
	}

	for _, tc := range []struct {
		name    string
		tag     string
		pinned  ocispec.Descriptor
		want    ocispec.Descriptor
		wantErr string
	}{
		{name: "tag", tag: "v1", want: v1.Descriptor},
		{name: "no tag", want: latest.Descriptor},
		{name: "pinned", tag: "latest", pinned: v1.Descriptor, want: v1.Descriptor},
		{name: "missing tag", tag: "v2", wantErr: `tag "v2": not found`},
	} {
		for _, source := range sources {
			if tc.tag != "" && tc.pinned.Digest == "" && !source.tagged {
				continue
			}
			t.Run(tc.name+"/"+source.name, func(t *testing.T) {
				loc := source.at(tc.tag)
				res, err := accessor.Resolve(ctx, &loc)
				require.NoError(t, err)
				res.Descriptor = tc.pinned

				img, err := accessor.Load(ctx, res, platforms.All)
				if tc.wantErr != "" {
					require.ErrorContains(t, err, tc.wantErr)
					return
				}
				require.NoError(t, err)
				assert.Equal(t, tc.want, img.Descriptor)
				require.NoError(t, img.Close())

				imgs, err := accessor.LoadAll(ctx, res, platforms.All)
				require.NoError(t, err)
				require.Len(t, imgs, 1)
				assert.Equal(t, tc.want, imgs[0].Descriptor)
				require.NoError(t, imgs[0].Close())
			})
		}
	}
}

func TestDeleteArchiveOfSeveral(t *testing.T) {
	ctx := t.Context()
	accessor := NewAccessor()

	layout := t.TempDir()
	for _, tag := range []string{"v1", "v2"} {
		_, err := accessor.Save(ctx, &Location{Scheme: schemes.OCILayout, Path: layout + ":" + tag},
			platformImages(ocispec.Platform{OS: tag, Architecture: "x86_64"})...)
		require.NoError(t, err)
	}
	tarPath := filepath.Join(t.TempDir(), "layout.tar")
	require.NoError(t, os.WriteFile(tarPath, tarLayout(t, layout), 0o644))

	err := accessor.Delete(ctx, &Location{Scheme: schemes.OCIArchive, Path: tarPath + ":v1"})
	require.ErrorContains(t, err, "holds other images")
	require.FileExists(t, tarPath)

	require.NoError(t, accessor.Delete(ctx, &Location{Scheme: schemes.OCIArchive, Path: tarPath}))
	require.NoFileExists(t, tarPath)
}

func TestArchiveUntagged(t *testing.T) {
	ctx := t.Context()
	accessor := NewAccessor()
	path := filepath.Join(t.TempDir(), "app.tar")
	root, err := SaveTarball(ctx, path, testImage)
	require.NoError(t, err)

	tagged := &Location{Scheme: schemes.OCIArchive, Path: path + ":v1"}
	_, err = accessor.Load(ctx, Resolved{Location: *tagged}, platforms.All)
	require.ErrorIs(t, err, errdefs.ErrNotFound)
	require.ErrorIs(t, accessor.Delete(ctx, tagged), errdefs.ErrNotFound)
	require.FileExists(t, path)

	img, err := accessor.Load(ctx, Resolved{Location: Location{Scheme: schemes.OCIArchive, Path: path}}, platforms.All)
	require.NoError(t, err)
	assert.Equal(t, root, img.Descriptor)
	require.NoError(t, img.Close())
}

func TestArchiveTag(t *testing.T) {
	ctx := t.Context()
	accessor := NewAccessor()
	dir := filepath.Join(t.TempDir(), "a:b")
	if runtime.GOOS == "windows" {
		dir = filepath.Join(t.TempDir(), "ab")
	}
	require.NoError(t, os.Mkdir(dir, 0o755))
	path := filepath.Join(dir, "app.tar")
	at := func(tag string) *Location {
		return &Location{Scheme: schemes.OCIArchive, Path: path + tag}
	}

	for _, tc := range []struct {
		name string
		save string
		load []string
		miss string
	}{
		{name: "tagged", save: ":v1", load: []string{":v1", ""}, miss: ":latest"},
		{name: "untagged", save: "", load: []string{":latest", ""}, miss: ":v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved, err := accessor.Save(ctx, at(tc.save), testImage)
			require.NoError(t, err)

			img, err := accessor.Load(ctx, saved, platforms.All)
			require.NoError(t, err)
			assert.Equal(t, saved, img.Resolved)
			require.NoError(t, img.Close())

			for _, tag := range tc.load {
				img, err := accessor.Load(ctx, Resolved{Location: *at(tag)}, platforms.All)
				require.NoError(t, err)
				assert.Equal(t, saved.Descriptor, img.Descriptor, "loading %q", tag)
				require.NoError(t, img.Close())
			}

			_, err = accessor.Load(ctx, Resolved{Location: *at(tc.miss)}, platforms.All)
			require.ErrorIs(t, err, errdefs.ErrNotFound)
			require.ErrorContains(t, err, path)

			require.ErrorIs(t, accessor.Delete(ctx, at(tc.miss)), errdefs.ErrNotFound)
			require.FileExists(t, path)

			require.NoError(t, accessor.Delete(ctx, at(tc.save)))
			require.NoFileExists(t, path)
		})
	}
}

// manifestLayout writes a layout whose index.json lists the manifest of an
// image of each platform, with no name, and returns it with that index.json.
func manifestLayout(t *testing.T, ps ...ocispec.Platform) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	store, err := local.NewStore(dir)
	require.NoError(t, err)

	index := ocispec.Index{SchemaVersion: 2, MediaType: ocispec.MediaTypeImageIndex}
	for _, img := range platformImages(ps...) {
		desc, err := SaveManifest(t.Context(), store, "test", img)
		require.NoError(t, err)
		index.Manifests = append(index.Manifests, desc)
	}
	data, err := json.Marshal(index)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ocispec.ImageIndexFile), data, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ocispec.ImageLayoutFile), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644))
	return dir, data
}

// layoutSources is the layout as a directory, as a tarball and as that tarball
// served over HTTP.
func layoutSources(t *testing.T, layout string) map[string]Location {
	t.Helper()
	tarball := tarLayout(t, layout)
	tarPath := filepath.Join(t.TempDir(), "layout.tar")
	require.NoError(t, os.WriteFile(tarPath, tarball, 0o644))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", MediaTypeOCILayoutTar)
		_, _ = w.Write(tarball)
	}))
	t.Cleanup(srv.Close)
	return map[string]Location{
		"layout":  {Scheme: schemes.OCILayout, Path: layout},
		"archive": {Scheme: schemes.OCIArchive, Path: tarPath},
		"http":    {Scheme: schemes.HTTPOCI, Path: srv.Listener.Addr().String() + "/ns/app/latest"},
	}
}

func TestLoadIndexAsRoot(t *testing.T) {
	ctx := t.Context()
	accessor := NewAccessor()
	fcIntel := ocispec.Platform{OS: "fc", Architecture: "x86_64"}
	fcArm := ocispec.Platform{OS: "fc", Architecture: "arm64"}

	layout, index := manifestLayout(t, fcIntel, fcArm)
	for name, loc := range layoutSources(t, layout) {
		t.Run(name, func(t *testing.T) {
			img, err := accessor.Load(ctx, Resolved{Location: loc}, platforms.Only(fcArm))
			require.NoError(t, err)
			assert.Equal(t, fcArm, img.Image.Platform)
			assert.Equal(t, ociindex.Descriptor(index), img.Descriptor, "the index.json is the image index")
			require.NoError(t, img.Close())

			imgs, err := accessor.LoadAll(ctx, img.Resolved, platforms.All)
			require.NoError(t, err)
			require.Len(t, imgs, 2)
			for _, img := range imgs {
				require.NoError(t, img.Close())
			}
		})
	}
}

func TestLoadManifestRootIgnoresPlatform(t *testing.T) {
	ctx := t.Context()
	accessor := NewAccessor()
	fcIntel := ocispec.Platform{OS: "fc", Architecture: "x86_64"}
	fcArm := ocispec.Platform{OS: "fc", Architecture: "arm64"}

	layout, _ := manifestLayout(t, fcArm)
	for name, loc := range layoutSources(t, layout) {
		t.Run(name, func(t *testing.T) {
			img, err := accessor.Load(ctx, Resolved{Location: loc}, platforms.OnlyStrict(fcIntel))
			require.NoError(t, err, "a root that is a manifest is not checked against the platform, as for a registry")
			assert.Equal(t, fcArm, img.Image.Platform)
			require.NoError(t, img.Close())
		})
	}
}

func TestArchiveColonInName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a file name cannot hold a colon")
	}
	ctx := t.Context()
	accessor := NewAccessor()
	dir := t.TempDir()

	sibling := filepath.Join(dir, "app")
	require.NoError(t, os.WriteFile(sibling, []byte("keep"), 0o644))
	for _, name := range []string{"app:1.0.tgz", "app:1.0.oci"} {
		_, err := accessor.Save(ctx, &Location{Scheme: schemes.OCIArchive, Path: filepath.Join(dir, name)}, testImage)
		require.NoError(t, err)
		require.FileExists(t, filepath.Join(dir, name))
	}
	kept, err := os.ReadFile(sibling)
	require.NoError(t, err)
	require.Equal(t, "keep", string(kept))

	named := &Location{Scheme: schemes.OCIArchive, Path: filepath.Join(dir, "app:1.0.tar")}
	saved, err := accessor.Save(ctx, named, testImage)
	require.NoError(t, err)
	require.FileExists(t, named.Path)

	// A file that was saved under a tag before the tag was split off.
	old := &Location{Scheme: schemes.OCIArchive, Path: filepath.Join(dir, "app.tar:v1")}
	require.NoError(t, os.Rename(named.Path, old.Path))
	img, err := accessor.Load(ctx, Resolved{Location: *old}, platforms.All)
	require.NoError(t, err)
	assert.Equal(t, saved.Descriptor, img.Descriptor)
	require.NoError(t, img.Close())
}

type failingFile struct{ File }

func (failingFile) Open(context.Context) (io.ReadCloser, int64, error) {
	return nil, 0, errors.New("no kernel today")
}

func (f failingFile) Clone() (File, error) { return f, nil }

func TestSaveArchiveKeepsWhatWasThere(t *testing.T) {
	ctx := t.Context()
	accessor := NewAccessor()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.tar")
	at := func(tag string) *Location {
		return &Location{Scheme: schemes.OCIArchive, Path: path + tag}
	}
	fcIntel := ocispec.Platform{OS: "fc", Architecture: "x86_64"}
	qemuIntel := ocispec.Platform{OS: "qemu", Architecture: "x86_64"}
	holds := func(want Resolved) {
		t.Helper()
		img, err := accessor.Load(ctx, Resolved{Location: want.Location}, platforms.All)
		require.NoError(t, err)
		assert.Equal(t, want.Descriptor, img.Descriptor)
		require.NoError(t, img.Close())
		files, err := os.ReadDir(dir)
		require.NoError(t, err)
		require.Len(t, files, 1, "nothing is left beside the tarball")
	}

	v1, err := accessor.Save(ctx, at(":v1"), platformImages(fcIntel)...)
	require.NoError(t, err)

	_, err = accessor.Save(ctx, at(":v2"), platformImages(qemuIntel)...)
	require.ErrorContains(t, err, "holds other images")
	holds(v1)

	broken := NewImage(WithKernel(failingFile{NewStaticFile("kernel.img", nil)}), WithPlatform(qemuIntel))
	_, err = accessor.Save(ctx, at(":v1"), broken)
	require.ErrorContains(t, err, "no kernel today")
	holds(v1)

	v1, err = accessor.Save(ctx, at(":v1"), platformImages(qemuIntel)...)
	require.NoError(t, err, "the same tag is replaced")
	holds(v1)

	latest, err := accessor.Save(ctx, at(""), platformImages(fcIntel)...)
	require.NoError(t, err, "no tag replaces the file")
	holds(latest)
	_, err = accessor.Load(ctx, Resolved{Location: *at(":v1")}, platforms.All)
	require.ErrorIs(t, err, errdefs.ErrNotFound)
}

func TestSaveArchiveInPlace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs file modes and symlinks")
	}
	ctx := t.Context()
	accessor := NewAccessor()
	archive := func(path string) *Location {
		return &Location{Scheme: schemes.OCIArchive, Path: path}
	}
	loads := func(t *testing.T, path string, want Resolved) {
		t.Helper()
		img, err := accessor.Load(ctx, Resolved{Location: *archive(path)}, platforms.All)
		require.NoError(t, err)
		assert.Equal(t, want.Descriptor, img.Descriptor)
		require.NoError(t, img.Close())
	}
	mode := func(t *testing.T, path string) os.FileMode {
		t.Helper()
		info, err := os.Stat(path)
		require.NoError(t, err)
		return info.Mode().Perm()
	}

	t.Run("the mode of the old file is kept", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "app.tar")
		_, err := accessor.Save(ctx, archive(path), testImage)
		require.NoError(t, err)
		require.NoError(t, os.Chmod(path, 0o600))

		_, err = accessor.Save(ctx, archive(path), testImage)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), mode(t, path))
	})

	t.Run("a new file gets the mode of any new file", func(t *testing.T) {
		dir := t.TempDir()
		f, err := os.Create(filepath.Join(dir, "other"))
		require.NoError(t, err)
		require.NoError(t, f.Close())

		_, err = accessor.Save(ctx, archive(filepath.Join(dir, "app.tar")), testImage)
		require.NoError(t, err)
		require.Equal(t, mode(t, filepath.Join(dir, "other")), mode(t, filepath.Join(dir, "app.tar")))

		broken := NewImage(WithKernel(failingFile{NewStaticFile("kernel.img", nil)}))
		_, err = accessor.Save(ctx, archive(filepath.Join(dir, "broken.tar")), broken)
		require.Error(t, err)
		require.NoFileExists(t, filepath.Join(dir, "broken.tar"))
	})

	t.Run("a symlink is saved through", func(t *testing.T) {
		dir := t.TempDir()
		target, link := filepath.Join(dir, "target.tar"), filepath.Join(dir, "link.tar")
		require.NoError(t, os.Symlink("target.tar", link))

		for range 2 {
			saved, err := accessor.Save(ctx, archive(link), testImage)
			require.NoError(t, err)
			info, err := os.Lstat(link)
			require.NoError(t, err)
			require.NotZero(t, info.Mode()&os.ModeSymlink)
			loads(t, target, saved)
		}

		broken := NewImage(WithKernel(failingFile{NewStaticFile("kernel.img", nil)}))
		dangling := filepath.Join(dir, "dangling.tar")
		require.NoError(t, os.Symlink("missing.tar", dangling))
		_, err := accessor.Save(ctx, archive(dangling), broken)
		require.Error(t, err)
		require.NoFileExists(t, filepath.Join(dir, "missing.tar"))
		_, err = os.Lstat(dangling)
		require.NoError(t, err)
	})

	t.Run("a device is written to", func(t *testing.T) {
		if _, err := os.Stat(os.DevNull); err != nil {
			t.Skip(err)
		}
		_, err := accessor.Save(ctx, archive(os.DevNull), testImage)
		require.NoError(t, err)
	})

	t.Run("a file in a directory that cannot be written", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes anywhere")
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "app.tar")
		require.NoError(t, os.WriteFile(path, nil, 0o644))
		require.NoError(t, os.Chmod(dir, 0o555))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

		saved, err := accessor.Save(ctx, archive(path), testImage)
		require.NoError(t, err)
		loads(t, path, saved)
	})
}

func TestDeleteArchiveWithParts(t *testing.T) {
	ctx := t.Context()
	accessor := NewAccessor()

	// As oras writes a layout: the tagged index, and its manifest beside it with no name.
	layout := t.TempDir()
	v1, err := accessor.Save(ctx, &Location{Scheme: schemes.OCILayout, Path: layout + ":v1"}, testImage)
	require.NoError(t, err)
	blob, err := os.ReadFile(filepath.Join(layout, "blobs", v1.Descriptor.Digest.Algorithm().String(), v1.Descriptor.Digest.Encoded()))
	require.NoError(t, err)
	var image, index ocispec.Index
	require.NoError(t, json.Unmarshal(blob, &image))
	data, err := os.ReadFile(filepath.Join(layout, ocispec.ImageIndexFile))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &index))
	index.Manifests = append(index.Manifests, image.Manifests...)
	data, err = json.Marshal(index)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(layout, ocispec.ImageIndexFile), data, 0o644))

	tarPath := filepath.Join(t.TempDir(), "app.tar")
	require.NoError(t, os.WriteFile(tarPath, tarLayout(t, layout), 0o644))
	tagged := &Location{Scheme: schemes.OCIArchive, Path: tarPath + ":v1"}

	img, err := accessor.Load(ctx, Resolved{Location: *tagged}, platforms.All)
	require.NoError(t, err)
	assert.Equal(t, v1.Descriptor, img.Descriptor)
	require.NoError(t, img.Close())

	require.NoError(t, accessor.Delete(ctx, tagged))
	require.NoFileExists(t, tarPath)
}

func TestDeleteArchiveReadsWhatALoadReads(t *testing.T) {
	ctx := t.Context()
	accessor := NewAccessor()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.tar")
	tagged := &Location{Scheme: schemes.OCIArchive, Path: path + ":v1"}
	_, err := accessor.Save(ctx, tagged, testImage)
	require.NoError(t, err)

	// rewrite copies the tarball, leaving out the entries named skip, and adds extra at its end.
	rewrite := func(t *testing.T, skip string, extra map[string][]byte) {
		t.Helper()
		saved, err := os.ReadFile(path)
		require.NoError(t, err)
		var out bytes.Buffer
		tw := tar.NewWriter(&out)
		tr := tar.NewReader(bytes.NewReader(saved))
		for {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			require.NoError(t, err)
			if hdr.Name == skip {
				continue
			}
			require.NoError(t, tw.WriteHeader(hdr))
			_, err = io.Copy(tw, tr)
			require.NoError(t, err)
		}
		for name, data := range extra {
			require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}))
			_, err := tw.Write(data)
			require.NoError(t, err)
		}
		require.NoError(t, tw.Close())
		require.NoError(t, os.WriteFile(path, out.Bytes(), 0o644))
	}

	t.Run("the last index.json", func(t *testing.T) {
		f, err := os.Open(path)
		require.NoError(t, err)
		_, r, err := readTarFile(tar.NewReader(f), ocispec.ImageIndexFile)
		require.NoError(t, err)
		var index ocispec.Index
		require.NoError(t, json.NewDecoder(r).Decode(&index))
		require.NoError(t, f.Close())

		other := index.Manifests[0]
		other.Annotations = map[string]string{ocispec.AnnotationRefName: "v2"}
		index.Manifests = append(index.Manifests, other)
		data, err := json.Marshal(index)
		require.NoError(t, err)
		rewrite(t, "", map[string][]byte{ocispec.ImageIndexFile: data})

		img, err := accessor.Load(ctx, Resolved{Location: Location{Scheme: schemes.OCIArchive, Path: path + ":v2"}}, platforms.All)
		require.NoError(t, err, "a load reads the last index.json")
		require.NoError(t, img.Close())

		require.ErrorContains(t, accessor.Delete(ctx, tagged), "holds other images")
		_, err = accessor.Save(ctx, tagged, testImage)
		require.ErrorContains(t, err, "holds other images")
		require.FileExists(t, path)
	})

	t.Run("not a layout", func(t *testing.T) {
		rewrite(t, ocispec.ImageLayoutFile, nil)

		_, loadErr := accessor.Load(ctx, Resolved{Location: *tagged}, platforms.All)
		require.Error(t, loadErr)
		require.Error(t, accessor.Delete(ctx, tagged))
		require.FileExists(t, path)
	})
}

func TestIndexSizeLimit(t *testing.T) {
	ctx := t.Context()
	accessor := NewAccessor()
	writeLayout := func(t *testing.T, layout string, index []byte) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(layout, ocispec.ImageIndexFile), index, 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(layout, ocispec.ImageLayoutFile), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644))
	}

	t.Run("too large", func(t *testing.T) {
		layout := t.TempDir()
		writeLayout(t, layout, append(bytes.Repeat([]byte(" "), int(docker.MaxManifestSize)), "{}"...))
		tarPath := filepath.Join(t.TempDir(), "app.tar")
		require.NoError(t, os.WriteFile(tarPath, tarLayout(t, layout), 0o644))

		for name, loc := range map[string]*Location{
			"layout":  {Scheme: schemes.OCILayout, Path: layout + ":v1"},
			"archive": {Scheme: schemes.OCIArchive, Path: tarPath + ":v1"},
		} {
			t.Run(name, func(t *testing.T) {
				_, loadErr := accessor.Load(ctx, Resolved{Location: *loc}, platforms.All)
				_, saveErr := accessor.Save(ctx, loc, testImage)
				for _, err := range []error{loadErr, saveErr, accessor.Delete(ctx, loc)} {
					require.ErrorIs(t, err, ociindex.ErrTooLarge)
					require.ErrorContains(t, err, "index.json")
					require.ErrorContains(t, err, "is larger than 4.2 MiB")
					require.NotContains(t, err.Error(), "too large")
				}
			})
		}
		require.FileExists(t, tarPath)
	})

	t.Run("as many entries as are taken", func(t *testing.T) {
		layout := t.TempDir()
		v1, err := accessor.Save(ctx, &Location{Scheme: schemes.OCILayout, Path: layout + ":v1"}, testImage)
		require.NoError(t, err)
		data, err := os.ReadFile(filepath.Join(layout, ocispec.ImageIndexFile))
		require.NoError(t, err)
		var index ocispec.Index
		require.NoError(t, json.Unmarshal(data, &index))
		for i := range 9999 {
			tag := "v" + strconv.Itoa(i+2)
			index.Manifests = append(index.Manifests, ocispec.Descriptor{
				MediaType: ocispec.MediaTypeImageManifest,
				Digest:    digest.FromString(tag),
				Size:      1234,
				Platform:  &ocispec.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"},
				Annotations: map[string]string{
					ocispec.AnnotationRefName:  tag,
					"io.containerd.image.name": "registry.example.org/some/repository/of/an/image:" + tag,
				},
			})
		}
		data, err = json.Marshal(index)
		require.NoError(t, err)
		require.Greater(t, int64(len(data)), docker.MaxManifestSize*3/4)
		require.LessOrEqual(t, int64(len(data)), docker.MaxManifestSize)
		writeLayout(t, layout, data)
		tarPath := filepath.Join(t.TempDir(), "app.tar")
		require.NoError(t, os.WriteFile(tarPath, tarLayout(t, layout), 0o644))

		for _, loc := range []Location{
			{Scheme: schemes.OCILayout, Path: layout + ":v1"},
			{Scheme: schemes.OCIArchive, Path: tarPath + ":v1"},
		} {
			img, err := accessor.Load(ctx, Resolved{Location: loc}, platforms.All)
			require.NoError(t, err, loc.Scheme)
			assert.Equal(t, v1.Descriptor, img.Descriptor)
			require.NoError(t, img.Close())
		}
	})
}
