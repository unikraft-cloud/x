// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/buildkit/client/llb"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	archivefs "github.com/unikraft/go-archivefs"
	"github.com/unikraft/go-archivefs/cpio"
	goerofs "github.com/unikraft/go-archivefs/erofs"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/kraftfile"

	"unikraft.com/x/build/internal/buildfs"
)

func TestDetectSourceTypeEmpty(t *testing.T) {
	_, err := DetectSourceType("")
	require.ErrorContains(t, err, "empty rootfs path")
}

func TestDetectSourceTypeDirectory(t *testing.T) {
	dir := writeTestDirectory(t)

	typ, err := DetectSourceType(dir)
	require.NoError(t, err)
	require.Equal(t, kraftfile.SourceTypeDirectory, typ)
}

func TestDetectSourceTypeCpio(t *testing.T) {
	cpioPath := writeTestCpioFile(t)

	typ, err := DetectSourceType(cpioPath)
	require.NoError(t, err)
	require.Equal(t, kraftfile.SourceTypeCpio, typ)
}

func TestDetectSourceTypeErofs(t *testing.T) {
	erofsPath := writeTestErofsFile(t)

	typ, err := DetectSourceType(erofsPath)
	require.NoError(t, err)
	require.Equal(t, kraftfile.SourceTypeErofs, typ)
}

func TestDetectSourceTypeTarball(t *testing.T) {
	tarPath := writeTestTarballFile(t)

	typ, err := DetectSourceType(tarPath)
	require.NoError(t, err)
	require.Equal(t, kraftfile.SourceTypeTarball, typ)
}

func TestDetectSourceTypeUnknown(t *testing.T) {
	p := filepath.Join(t.TempDir(), "random.bin")
	require.NoError(t, os.WriteFile(p, []byte("not an archive"), 0o644))

	_, err := DetectSourceType(p)
	require.ErrorContains(t, err, "could not detect file rootfs type")
}

func TestRootfsCpioArchive(t *testing.T) {
	cpioPath := writeTestCpioFile(t)

	imgs := runBuildRootfs(t, BuildOpts{
		Rootfs: FSOpts{
			Path: cpioPath,
			Type: kraftfile.SourceTypeCpio,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.Len(t, imgs, 1)

	// The pre-built CPIO is passed through as-is; verify its contents.
	files := readCpioInitrd(t, imgs[0])
	require.Contains(t, files, "hello.txt")
	require.Equal(t, "hello\n", files["hello.txt"])
	require.Contains(t, files, "subdir/nested.txt")
	require.Equal(t, "nested\n", files["subdir/nested.txt"])
}

func TestRootfsErofsArchive(t *testing.T) {
	erofsPath := writeTestErofsFile(t)

	imgs := runBuildRootfs(t, BuildOpts{
		Rootfs: FSOpts{
			Path: erofsPath,
			Type: kraftfile.SourceTypeErofs,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.Len(t, imgs, 1)

	// The pre-built EROFS is passed through as-is; verify its contents.
	files := readErofsInitrd(t, imgs[0])
	require.Contains(t, files, "hello.txt")
	require.Equal(t, "hello\n", files["hello.txt"])
	require.Contains(t, files, "subdir/nested.txt")
	require.Equal(t, "nested\n", files["subdir/nested.txt"])
}

func TestRootfsTarball(t *testing.T) {
	tarPath := writeTestTarballFile(t)

	for _, format := range []kraftfile.FsType{kraftfile.FsTypeCpio, kraftfile.FsTypeErofs} {
		t.Run(string(format), func(t *testing.T) {
			imgs := runBuildRootfs(t, BuildOpts{
				Rootfs: FSOpts{
					Path:   tarPath,
					Type:   kraftfile.SourceTypeTarball,
					Format: format,
				},
				Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
			})
			require.Len(t, imgs, 1)

			switch format {
			case kraftfile.FsTypeCpio:
				files := readCpioInitrd(t, imgs[0])
				require.Contains(t, files, "hello.txt")
				require.Equal(t, "hello\n", files["hello.txt"])
				require.Contains(t, files, "subdir/nested.txt")
				require.Equal(t, "nested\n", files["subdir/nested.txt"])
			case kraftfile.FsTypeErofs:
				files := readErofsInitrd(t, imgs[0])
				require.Contains(t, files, "hello.txt")
				require.Equal(t, "hello\n", files["hello.txt"])
				require.Contains(t, files, "subdir/nested.txt")
				require.Equal(t, "nested\n", files["subdir/nested.txt"])
			}
		})
	}
}

// writeLinkedTarball writes a tarball of a file of size bytes and links more
// hardlinks to it, and returns its path.
func writeLinkedTarball(t *testing.T, size, links int) string {
	t.Helper()

	tarPath := filepath.Join(t.TempDir(), "linked.tar")
	f, err := os.Create(tarPath)
	require.NoError(t, err)
	defer f.Close()
	tw := tar.NewWriter(f)
	defer tw.Close()

	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "file", Mode: 0o644, Size: int64(size), Typeflag: tar.TypeReg}))
	_, err = tw.Write(make([]byte, size))
	require.NoError(t, err)
	for i := range links {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("link%d", i), Mode: 0o644, Typeflag: tar.TypeLink, Linkname: "file"}))
	}

	return tarPath
}

// TestRootfsTooMuchData asserts that a rootfs whose files hold more than
// MaxRootfsSize is refused before it is packed, each file with hardlinks once.
func TestRootfsTooMuchData(t *testing.T) {
	for _, tc := range []struct {
		name  string
		links int
		limit int64
		want  error
	}{
		{"more data than the limit is too large", 0, 1000, ErrTooLarge},
		{"a file and its hardlinks count once", 3, 1500, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := BuildOpts{
				Rootfs: FSOpts{
					Path:   writeLinkedTarball(t, 1024, tc.links),
					Type:   kraftfile.SourceTypeTarball,
					Format: kraftfile.FsTypeCpio,
				},
				Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
			}

			imgs, err := Builder{TempDir: t.TempDir(), MaxRootfsSize: tc.limit}.BuildRootfs(t.Context(), opts)
			for _, img := range imgs {
				require.NoError(t, img.Close())
			}
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRootfsNoPlatform(t *testing.T) {
	cpioPath := writeTestCpioFile(t)
	ctx := t.Context()

	_, err := Builder{}.BuildRootfs(ctx, BuildOpts{
		Rootfs: FSOpts{
			Path: cpioPath,
			Type: kraftfile.SourceTypeCpio,
		},
	})
	require.ErrorContains(t, err, "at least one platform must be specified")
}

func TestRootfsPreservesConfig(t *testing.T) {
	dir := writeTestDirectory(t)

	imgs := runBuildRootfs(t, BuildOpts{
		Rootfs: FSOpts{
			Path:   dir,
			Type:   kraftfile.SourceTypeDirectory,
			Format: kraftfile.FsTypeCpio,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
		Cmd:      []string{"/bin/app", "--flag"},
		Env: kraftfile.Map{
			{Key: "FOO", Value: "bar"},
		},
		Labels: map[string]string{"test": "value"},
	})
	require.Len(t, imgs, 1)
	require.NotNil(t, imgs[0])
	require.NotNil(t, imgs[0].Image)
	require.Equal(t, []string{"/bin/app", "--flag"}, imgs[0].Image.Config.Cmd)
	require.Contains(t, imgs[0].Image.Config.Env, "FOO=bar")
	require.Equal(t, map[string]string{"test": "value"}, imgs[0].Image.Config.Labels)

	// Config assertions above are sufficient, but also verify the initrd is valid.
	files := readCpioInitrd(t, imgs[0])
	require.Contains(t, files, "hello.txt")
}

func TestRootfsDirectoryDefaultFormat(t *testing.T) {
	dir := writeTestDirectory(t)

	imgs := runBuildRootfs(t, BuildOpts{
		Rootfs: FSOpts{
			Path: dir,
			Type: kraftfile.SourceTypeDirectory,
			// Format intentionally omitted; kernel-less builds default to erofs.
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.Len(t, imgs, 1)

	files := readErofsInitrd(t, imgs[0])
	require.Contains(t, files, "hello.txt")
	require.Equal(t, "hello\n", files["hello.txt"])
	require.Contains(t, files, "subdir/nested.txt")
	require.Equal(t, "nested\n", files["subdir/nested.txt"])
}

func TestRomDefaultFormat(t *testing.T) {
	dir := writeTestDirectory(t)

	ctx := t.Context()

	romFiles, err := Builder{}.buildRoms(ctx, BuildOpts{
		Roms: []FSOpts{
			{
				Path: dir,
				Type: kraftfile.SourceTypeDirectory,
				// Format intentionally omitted; should default to erofs.
			},
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.NoError(t, err)
	require.Len(t, romFiles, 1)
	require.Len(t, romFiles[0], 1) // one platform: the default
	t.Cleanup(func() { _ = romFiles[0][0].Cleanup() })

	// Verify the rom was built as EROFS by reading it back.
	rc, _, err := romFiles[0][0].Open(ctx)
	require.NoError(t, err)
	defer rc.Close()

	tmp, err := os.CreateTemp(t.TempDir(), "rom-*.img")
	require.NoError(t, err)
	defer tmp.Close()

	_, err = io.Copy(tmp, rc)
	require.NoError(t, err)

	fsys, err := goerofs.Open(tmp)
	require.NoError(t, err)

	files := readFiles(t, fsys)
	require.Contains(t, files, "hello.txt")
	require.Equal(t, "hello\n", files["hello.txt"])
	require.Contains(t, files, "subdir/nested.txt")
	require.Equal(t, "nested\n", files["subdir/nested.txt"])
}

// A ROM built from a Dockerfile is built from the one it names, which is
// looked for before anything connects to BuildKit.
func TestRomDockerfile(t *testing.T) {
	dir := writeTestDirectory(t)

	_, err := Builder{}.buildRoms(t.Context(), BuildOpts{
		Roms: []FSOpts{
			{Path: dir, Type: kraftfile.SourceTypeDockerfile, Dockerfile: "Dockerfile.rom"},
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.ErrorContains(t, err, `dockerfile "Dockerfile.rom" does not exist`)
}

// A ROM payload is a compiled artifact, so a multi-platform build must produce
// one ROM per platform rather than reusing a single one everywhere. Reusing it
// silently ships, say, an x86_64 binary inside the arm64 image.
func TestRomPerPlatform(t *testing.T) {
	dir := writeTestDirectory(t)

	ctx := t.Context()

	plats := []ocispec.Platform{
		{Architecture: "amd64", OS: "linux"},
		{Architecture: "arm64", OS: "linux"},
	}

	romFiles, err := Builder{}.buildRoms(ctx, BuildOpts{
		Roms: []FSOpts{
			{
				Path: dir,
				Type: kraftfile.SourceTypeDirectory,
			},
		},
		Platform: plats,
	})
	require.NoError(t, err)
	require.Len(t, romFiles, 1)
	require.Len(t, romFiles[0], len(plats), "one rom per platform")

	t.Cleanup(func() {
		for _, f := range romFiles[0] {
			_ = f.Cleanup()
		}
	})

	// Each platform must own a distinct artifact. Sharing a single handle
	// across platforms is the bug this guards against: with a Dockerfile
	// source the payload is compiled per $TARGETARCH, so reusing one would
	// put the wrong architecture's binary in every image but the first.
	//
	// The bytes are legitimately identical here -- a directory source does
	// not vary by platform -- so the assertion is on the handles, not on
	// their contents.
	for i, f := range romFiles[0] {
		require.NotNil(t, f, "no rom for platform %d", i)
	}
	require.NotSame(t, romFiles[0][0], romFiles[0][1])
}

// A rom reads its Dockerfile from its own Dockerfile source, thus the path of
// the Dockerfile must stay inside that source.
func TestRomDockerfileSource(t *testing.T) {
	st := StateSource{State: llb.Scratch()}

	_, err := Builder{}.buildRoms(t.Context(), BuildOpts{
		Roms: []FSOpts{{
			Source:           st,
			Path:             "app",
			Type:             kraftfile.SourceTypeDockerfile,
			Dockerfile:       "../Dockerfile",
			DockerfileSource: st,
		}},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.ErrorContains(t, err, "outside the source")
}

func TestApplyConfigOverrides(t *testing.T) {
	base := ocispec.ImageConfig{
		Cmd:    []string{"/base"},
		Env:    []string{"PATH=/bin", "SHADOWED=base"},
		Labels: map[string]string{"base": "base", "shared": "base"},
	}
	opts := BuildOpts{
		Cmd: []string{"/override"},
		Env: kraftfile.Map{
			{Key: "FOO", Value: "bar"},
			{Key: "SHADOWED", Value: "opt"},
		},
		Labels: map[string]string{"opt": "opt", "shared": "opt"},
	}

	cfg := applyConfigOverrides(base, opts)
	require.Equal(t, []string{"/override"}, cfg.Cmd)
	require.Equal(t, []string{"PATH=/bin", "SHADOWED=opt", "FOO=bar"}, cfg.Env,
		"an entry of the caller must replace the entry of the base, not shadow it")
	require.Equal(t, opts.Labels, cfg.Labels,
		"labels must replace the base's, not merge with them")
}

func TestApplyConfigOverridesEmpty(t *testing.T) {
	base := ocispec.ImageConfig{
		Cmd:    []string{"/base"},
		Env:    []string{"PATH=/bin"},
		Labels: map[string]string{"base": "base"},
	}

	cfg := applyConfigOverrides(base, BuildOpts{})
	require.Equal(t, base.Cmd, cfg.Cmd)
	require.Equal(t, base.Env, cfg.Env)
	require.Equal(t, base.Labels, cfg.Labels,
		"the labels of the base must survive when the caller sets none")
}

func TestResolveSourceDockerfileConflictingType(t *testing.T) {
	fsOpts := FSOpts{Source: LocalSource{Dir: "/root"}, Path: "context", Dockerfile: "MyDockerfile", Type: kraftfile.SourceTypeTarball}
	require.ErrorContains(t, resolveSource(&fsOpts), "source type must be")
}

func TestResolveSourceOCIWithDockerfile(t *testing.T) {
	fsOpts := FSOpts{Source: LocalSource{Dir: "/root"}, Path: "index.unikraft.io/test/img:latest", Type: kraftfile.SourceTypeOCI, Dockerfile: "MyDockerfile"}
	require.ErrorContains(t, resolveSource(&fsOpts), "dockerfile cannot be set")
}

func TestRootfsUnsupportedType(t *testing.T) {
	ctx := t.Context()

	_, err := Builder{}.BuildRootfs(ctx, BuildOpts{
		Rootfs: FSOpts{
			Path: "/some/path",
			Type: kraftfile.SourceType("unsupported"),
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.ErrorContains(t, err, "unsupported rootfs type")
}

func TestRootfsCpioSourceErofsFormatMismatch(t *testing.T) {
	cpioPath := writeTestCpioFile(t)
	ctx := t.Context()

	_, err := Builder{}.BuildRootfs(ctx, BuildOpts{
		Rootfs: FSOpts{
			Path:   cpioPath,
			Type:   kraftfile.SourceTypeCpio,
			Format: kraftfile.FsTypeErofs,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.ErrorContains(t, err, "rootfs format mismatch")
}

// readCpioInitrd opens the initrd from img and returns a map of file name to
// content for all regular files in the CPIO archive.
func readCpioInitrd(t *testing.T, img *imagespec.Image) map[string]string {
	t.Helper()
	return readFiles(t, openImage(t, img.Initrd, kraftfile.FsTypeCpio))
}

// readErofsInitrd opens the initrd from img and returns a map of file name to
// content for all regular files in the EROFS archive.
func readErofsInitrd(t *testing.T, img *imagespec.Image) map[string]string {
	t.Helper()
	return readFiles(t, openImage(t, img.Initrd, kraftfile.FsTypeErofs))
}

// openImage opens the filesystem image f in format as an fs.FS.
func openImage(t *testing.T, f imagespec.File, format kraftfile.FsType) fs.FS {
	t.Helper()
	require.NotNil(t, f)

	rc, _, err := f.Open(t.Context())
	require.NoError(t, err)
	defer rc.Close()

	// Both readers need io.ReaderAt; copy the stream into a temporary file.
	tmp, err := os.CreateTemp(t.TempDir(), "rootfs-*.img")
	require.NoError(t, err)
	t.Cleanup(func() { tmp.Close() })
	_, err = io.Copy(tmp, rc)
	require.NoError(t, err)

	switch format {
	case kraftfile.FsTypeCpio:
		fsys, err := cpio.OpenFS(tmp)
		require.NoError(t, err)
		return fsys
	case kraftfile.FsTypeErofs:
		fsys, err := goerofs.Open(tmp)
		require.NoError(t, err)
		return fsys
	}
	t.Fatalf("unknown format %q", format)
	return nil
}

// readFiles returns a map of file name to content for all regular files in fsys.
func readFiles(t *testing.T, fsys fs.FS) map[string]string {
	t.Helper()

	files := make(map[string]string)
	require.NoError(t, fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		data, err := fs.ReadFile(fsys, path)
		files[path] = string(data)
		return err
	}))
	return files
}

// writeTestDirectory creates a temporary directory with some test files.
func writeTestDirectory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "subdir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "subdir", "nested.txt"), []byte("nested\n"), 0o644))
	return dir
}

// writeTestCpioFile creates a valid CPIO archive from a temporary directory.
func writeTestCpioFile(t *testing.T) string {
	t.Helper()
	srcDir := writeTestDirectory(t)

	cpioPath := filepath.Join(t.TempDir(), "test.cpio")
	f, err := os.Create(cpioPath)
	require.NoError(t, err)
	defer f.Close()

	require.NoError(t, buildfs.CreateCPIO(f, os.DirFS(srcDir)))
	return cpioPath
}

// writeTestErofsFile creates a valid EROFS archive from a temporary directory.
func writeTestErofsFile(t *testing.T) string {
	t.Helper()
	srcDir := writeTestDirectory(t)

	erofsPath := filepath.Join(t.TempDir(), "test.erofs")
	f, err := os.Create(erofsPath)
	require.NoError(t, err)
	defer f.Close()

	require.NoError(t, buildfs.CreateEROFS(f, os.DirFS(srcDir)))
	return erofsPath
}

// writeTestTarballFile creates a valid tarball from a temporary directory.
func writeTestTarballFile(t *testing.T) string {
	t.Helper()
	srcDir := writeTestDirectory(t)

	tarPath := filepath.Join(t.TempDir(), "test.tar")
	f, err := os.Create(tarPath)
	require.NoError(t, err)
	defer f.Close()

	tw := tar.NewWriter(f)
	defer tw.Close()

	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	require.NoError(t, err)

	return tarPath
}

// runBuildRootfs calls BuildRootfs and registers cleanup for the returned images.
func runBuildRootfs(t *testing.T, opts BuildOpts) []*imagespec.Image {
	t.Helper()

	imgs, err := Builder{}.BuildRootfs(t.Context(), opts)
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, img := range imgs {
			_ = img.Close()
		}
	})
	return imgs
}

func TestRootfsTarballKeepsOwners(t *testing.T) {
	tarPath := writeOwnedTarball(t, 1000, 1000)

	for _, format := range []kraftfile.FsType{kraftfile.FsTypeCpio, kraftfile.FsTypeErofs} {
		t.Run(string(format), func(t *testing.T) {
			imgs := runBuildRootfs(t, BuildOpts{
				Rootfs: FSOpts{
					Path:   tarPath,
					Type:   kraftfile.SourceTypeTarball,
					Format: format,
				},
				Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
			})
			require.Len(t, imgs, 1)

			owners := readOwners(t, imgs[0].Initrd, format)
			require.Equal(t, owner{1000, 1000}, owners["owned"])
			require.Equal(t, owner{1000, 1000}, owners["owned/file.txt"])
		})
	}
}

func TestRootfsDirectoryIsOwnedByRoot(t *testing.T) {
	srcDir := writeTestDirectory(t)

	for _, format := range []kraftfile.FsType{kraftfile.FsTypeCpio, kraftfile.FsTypeErofs} {
		t.Run(string(format), func(t *testing.T) {
			imgs := runBuildRootfs(t, BuildOpts{
				Rootfs: FSOpts{
					Path:   srcDir,
					Type:   kraftfile.SourceTypeDirectory,
					Format: format,
				},
				Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
			})
			require.Len(t, imgs, 1)

			// The checkout's owners mean nothing in the guest, directories included.
			owners := readOwners(t, imgs[0].Initrd, format)
			require.Contains(t, owners, "subdir")
			for name, o := range owners {
				require.Equal(t, owner{0, 0}, o, name)
			}
		})
	}
}

func TestRepackErofsAsCpio(t *testing.T) {
	tarPath := filepath.Join(t.TempDir(), "rootfs.tar")
	tarFile, err := os.Create(tarPath)
	require.NoError(t, err)
	tw := tar.NewWriter(tarFile)
	data := []byte("owned\n")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir, Name: "owned/", Mode: 0o750, Uid: 1000, Gid: 1000,
	}))
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg, Name: "owned/file.txt", Mode: 0o640,
		Size: int64(len(data)), Uid: 1000, Gid: 1000,
	}))
	_, err = tw.Write(data)
	require.NoError(t, err)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink, Name: "link", Linkname: "owned/file.txt", Mode: 0o777,
	}))
	require.NoError(t, tw.Close())
	require.NoError(t, tarFile.Close())

	imgs := runBuildRootfs(t, BuildOpts{
		Rootfs: FSOpts{
			Path:   tarPath,
			Type:   kraftfile.SourceTypeTarball,
			Format: kraftfile.FsTypeErofs,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.Len(t, imgs, 1)

	f, err := Builder{TempDir: t.TempDir()}.Repack(t.Context(), imgs[0].Initrd, imgs[0].Image.Platform, kraftfile.FsTypeCpio)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Cleanup() })

	fsys := openImage(t, f, kraftfile.FsTypeCpio)
	info, err := fs.Lstat(fsys, "owned/file.txt")
	require.NoError(t, err)
	require.Equal(t, owner{1000, 1000}, owner{archivefs.GetUID(info.Sys()), archivefs.GetGID(info.Sys())})
	require.Equal(t, fs.FileMode(0o640), info.Mode().Perm())
	content, err := fs.ReadFile(fsys, "owned/file.txt")
	require.NoError(t, err)
	require.Equal(t, "owned\n", string(content))
	target, err := fs.ReadLink(fsys, "link")
	require.NoError(t, err)
	require.Equal(t, "owned/file.txt", target)
}

func TestRepackSameFormat(t *testing.T) {
	imgs := runBuildRootfs(t, BuildOpts{
		Rootfs: FSOpts{
			Path:   writeTestDirectory(t),
			Type:   kraftfile.SourceTypeDirectory,
			Format: kraftfile.FsTypeCpio,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.Len(t, imgs, 1)

	f, err := Builder{TempDir: t.TempDir()}.Repack(t.Context(), imgs[0].Initrd, imgs[0].Image.Platform, kraftfile.FsTypeCpio)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Cleanup() })

	files := readCpioInitrd(t, &imagespec.Image{Initrd: f})
	require.Equal(t, "hello\n", files["hello.txt"])
	require.Equal(t, "nested\n", files["subdir/nested.txt"])
}

func TestRepackStream(t *testing.T) {
	data, err := os.ReadFile(writeTestErofsFile(t))
	require.NoError(t, err)

	// A static file opens as a stream, without random access.
	f, err := Builder{TempDir: t.TempDir()}.Repack(t.Context(), imagespec.NewStaticFile("rootfs.erofs", data),
		ocispec.Platform{OS: "fc", Architecture: "x86_64"}, kraftfile.FsTypeCpio)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Cleanup() })

	files := readCpioInitrd(t, &imagespec.Image{Initrd: f})
	require.Equal(t, "hello\n", files["hello.txt"])
	require.Equal(t, "nested\n", files["subdir/nested.txt"])
}

// owner is the uid and gid of an entry in a built root filesystem.
type owner struct{ uid, gid int }

// readOwners returns the owner of every entry in the filesystem image f.
func readOwners(t *testing.T, f imagespec.File, format kraftfile.FsType) map[string]owner {
	t.Helper()
	owners := make(map[string]owner)
	fsys := openImage(t, f, format)
	require.NoError(t, fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		owners[path] = owner{archivefs.GetUID(info.Sys()), archivefs.GetGID(info.Sys())}
		return nil
	}))
	return owners
}

// writeOwnedTarball writes a tarball whose directory and file belong to uid:gid.
func writeOwnedTarball(t *testing.T, uid, gid int) string {
	t.Helper()
	tarPath := filepath.Join(t.TempDir(), "owned.tar")
	f, err := os.Create(tarPath)
	require.NoError(t, err)
	defer f.Close()

	tw := tar.NewWriter(f)
	defer tw.Close()

	require.NoError(t, tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir, Name: "owned/", Mode: 0o755, Uid: uid, Gid: gid,
	}))
	data := []byte("owned\n")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg, Name: "owned/file.txt", Mode: 0o644,
		Size: int64(len(data)), Uid: uid, Gid: gid,
	}))
	_, err = tw.Write(data)
	require.NoError(t, err)
	return tarPath
}
