// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package uri_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/image-spec/uri"
)

func TestParse(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		src    string
		scheme uri.Scheme
		path   string
		err    bool
	}{
		{name: "oci", src: "oci://unikraft.io/app:latest", scheme: uri.SchemeOCI, path: "unikraft.io/app:latest"},
		{name: "oci-layout", src: "oci-layout://./layout", scheme: uri.SchemeOCILayout, path: "./layout"},
		{name: "oci-archive", src: "oci-archive://app.tar", scheme: uri.SchemeOCIArchive, path: "app.tar"},
		{name: "an unknown scheme is rejected", src: "docker://app", err: true},
		{name: "a bare reference is rejected", src: "unikraft.io/app", err: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := uri.Parse(tc.src)
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

func TestParseDefault(t *testing.T) {
	t.Parallel()

	got, err := uri.ParseDefault("unikraft.io/app:latest")
	require.NoError(t, err)
	assert.Equal(t, uri.SchemeOCI, got.Scheme, "a bare reference takes the default scheme")
	assert.Equal(t, "unikraft.io/app:latest", got.Path)

	got, err = uri.ParseDefault("oci-archive://app.tar")
	require.NoError(t, err)
	assert.Equal(t, uri.SchemeOCIArchive, got.Scheme, "a named scheme still wins")

	_, err = uri.ParseDefault("docker://app")
	assert.Error(t, err, "a named but unknown scheme is still rejected")
}

func TestGuess(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	layout := filepath.Join(dir, "layout")
	require.NoError(t, os.Mkdir(layout, 0o755))
	archive := filepath.Join(dir, "app.tar")
	require.NoError(t, os.WriteFile(archive, nil, 0o644))

	for _, tc := range []struct {
		name   string
		src    string
		scheme uri.Scheme
	}{
		{name: "a named scheme is taken as given", src: "oci://app", scheme: uri.SchemeOCI},
		{name: "an existing directory is a layout", src: layout, scheme: uri.SchemeOCILayout},
		{name: "an existing file is an archive", src: archive, scheme: uri.SchemeOCIArchive},
		{name: "a tarball name is an archive", src: "missing/app.tar.gz", scheme: uri.SchemeOCIArchive},
		{name: "a trailing separator is a layout", src: "missing/layout/", scheme: uri.SchemeOCILayout},
		{name: "anything else is a reference", src: "unikraft.io/app:latest", scheme: uri.SchemeOCI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := uri.Guess(tc.src)
			require.NoError(t, err)
			assert.Equal(t, tc.scheme, got.Scheme)
		})
	}

	_, err := uri.Guess("./missing")
	assert.Error(t, err, "a path that names nothing is ambiguous")
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

			path, tag := uri.SplitPathTag(tc.src)
			assert.Equal(t, tc.path, path)
			assert.Equal(t, tc.tag, tag)
		})
	}
}
