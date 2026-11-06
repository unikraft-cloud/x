// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/image-spec/schemes"
)

func TestParseLocation(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		src    string
		scheme schemes.Scheme
		path   string
		err    bool
	}{
		{name: "oci", src: "oci://unikraft.io/app:latest", scheme: schemes.OCI, path: "unikraft.io/app:latest"},
		{name: "oci-layout", src: "oci-layout://./layout", scheme: schemes.OCILayout, path: "./layout"},
		{name: "oci-archive", src: "oci-archive://app.tar", scheme: schemes.OCIArchive, path: "app.tar"},
		{name: "an unknown scheme is rejected", src: "docker://app", err: true},
		{name: "http layout", src: "https+oci://example.org/me/app/latest", scheme: schemes.HTTPSOCI, path: "example.org/me/app/latest"},
		{name: "a bare reference is rejected", src: "unikraft.io/app", err: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseLocation(tc.src)
			if tc.err {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.scheme, got.Scheme)
			assert.Equal(t, tc.path, got.Path)
			assert.Equal(t, tc.src, got.String())
		})
	}
}

func TestParseLocationDefault(t *testing.T) {
	t.Parallel()

	got, err := ParseLocationDefault("unikraft.io/app:latest")
	require.NoError(t, err)
	assert.Equal(t, schemes.OCI, got.Scheme, "a bare reference takes the default scheme")
	assert.Equal(t, "unikraft.io/app:latest", got.Path)

	got, err = ParseLocationDefault("oci-archive://app.tar")
	require.NoError(t, err)
	assert.Equal(t, schemes.OCIArchive, got.Scheme, "a named scheme still wins")

	_, err = ParseLocationDefault("docker://app")
	assert.Error(t, err, "a named but unknown scheme is still rejected")
}

func TestGuessLocation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	layout := filepath.Join(dir, "layout")
	require.NoError(t, os.Mkdir(layout, 0o755))
	archive := filepath.Join(dir, "app.tar")
	require.NoError(t, os.WriteFile(archive, nil, 0o644))

	for _, tc := range []struct {
		name   string
		src    string
		scheme schemes.Scheme
	}{
		{name: "a named scheme is taken as given", src: "oci://app", scheme: schemes.OCI},
		{name: "an existing directory is a layout", src: layout, scheme: schemes.OCILayout},
		{name: "an existing file is an archive", src: archive, scheme: schemes.OCIArchive},
		{name: "an existing directory with a tag is a layout", src: layout + ":v1", scheme: schemes.OCILayout},
		{name: "a tarball name is an archive", src: "./missing/app.tar.gz", scheme: schemes.OCIArchive},
		{name: "a parent tarball name is an archive", src: "../missing/app.tar", scheme: schemes.OCIArchive},
		{name: "a trailing separator is a layout", src: "./missing/layout/", scheme: schemes.OCILayout},
		{name: "the current directory is a layout", src: ".", scheme: schemes.OCILayout},
		{name: "a bare tarball name is a reference", src: "app.tar", scheme: schemes.OCI},
		{name: "a bare trailing separator is a reference", src: "missing/layout/", scheme: schemes.OCI},
		{name: "a dotted name is a reference", src: ".app", scheme: schemes.OCI},
		{name: "anything else is a reference", src: "unikraft.io/app:latest", scheme: schemes.OCI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := GuessLocation(tc.src)
			require.NoError(t, err)
			assert.Equal(t, tc.scheme, got.Scheme)
		})
	}

	_, err := GuessLocation("./missing")
	assert.Error(t, err, "a path that names nothing is ambiguous")
}

func TestGuessLocationIgnoresExistingBareNames(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.Mkdir("app", 0o755))
	require.NoError(t, os.WriteFile("app.tar", nil, 0o644))

	for _, src := range []string{"app", "app:latest", "app.tar"} {
		got, err := GuessLocation(src)
		require.NoError(t, err)
		assert.Equal(t, schemes.OCI, got.Scheme, src)
	}
}

func TestSplitPathTag(t *testing.T) {
	t.Parallel()

	type splitCase struct {
		name string
		src  string
		path string
		tag  string
	}
	cases := []splitCase{
		{name: "a path without a tag", src: "layout", path: "layout"},
		{name: "a path with a tag", src: "layout:v1", path: "layout", tag: "v1"},
		{name: "the last colon starts the tag", src: "./a:b:v1", path: "./a:b", tag: "v1"},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases, []splitCase{
			{name: "a drive letter is not a tag", src: `C:\out\layout`, path: `C:\out\layout`},
			{name: "a bare drive is not a tag", src: `D:`, path: `D:`},
			{name: "a tag after a drive letter", src: `C:\out\layout:v1`, path: `C:\out\layout`, tag: "v1"},
		}...)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path, tag := splitPathTag(tc.src)
			assert.Equal(t, tc.path, path)
			assert.Equal(t, tc.tag, tag)
		})
	}
}
