// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/session"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/kraftfile"
)

func TestResolveSourceStateRejects(t *testing.T) {
	st := StateSource{State: llb.Scratch()}

	for _, tc := range []struct {
		name string
		src  FSOpts
		err  string
	}{
		{"outside", FSOpts{Source: st, Path: "../rootfs"}, "outside the source"},
		{"absolute", FSOpts{Source: st, Path: "/rootfs"}, "must be relative"},
		{"tarball", FSOpts{Source: st, Path: "rootfs.tar", Type: kraftfile.SourceTypeTarball}, "cannot come from an LLB state"},
		{"pointer", FSOpts{Source: &st, Path: "rootfs"}, "unsupported source"},
		{"dockerfile outside", FSOpts{Source: st, Path: "app", Dockerfile: "../Dockerfile", DockerfileSource: st}, "outside the source"},
		{"dockerfile source without dockerfile", FSOpts{Source: st, Path: "app", Type: kraftfile.SourceTypeDockerfile, DockerfileSource: st}, "needs a dockerfile"},
		{"local dockerfile source", FSOpts{Source: st, Path: "app", Dockerfile: "Dockerfile", DockerfileSource: LocalSource{}}, "must both be LLB states"},
		{"session tarball", FSOpts{Source: SessionSource{Name: "context"}, Path: "rootfs.tar", Type: kraftfile.SourceTypeTarball}, "cannot come from a session"},
		{"session without name", FSOpts{Source: SessionSource{}, Path: "app"}, "needs a name"},
		{"session pattern path", FSOpts{Source: SessionSource{Name: "context"}, Path: "files[1]"}, "cannot start with ! or contain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorContains(t, resolveSource(&tc.src), tc.err)
		})
	}
}

func TestApplyBuildOptsStateSource(t *testing.T) {
	st := llb.Git("https://example.com/repo.git", "")

	for _, tc := range []struct {
		name   string
		rootfs FSOpts
		attrs  map[string]string
	}{
		{
			name:   "dockerfile path",
			rootfs: FSOpts{Path: "app/Dockerfile.prod"},
			attrs:  map[string]string{"filename": "app/Dockerfile.prod"},
		},
		{
			name:   "dockerfile field",
			rootfs: FSOpts{Path: "app", Dockerfile: "docker/Dockerfile"},
			attrs:  map[string]string{"filename": "app/docker/Dockerfile"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.rootfs.Source = StateSource{State: st}
			attrs := map[string]string{}
			localDirs := map[string]string{}
			inputs := map[string]llb.State{}

			err := applyBuildOpts(attrs, localDirs, inputs, new([]session.Attachable), BuildOpts{Rootfs: tc.rootfs})
			require.NoError(t, err)
			require.Equal(t, tc.attrs, attrs)
			require.Empty(t, localDirs)
			require.Len(t, inputs, 2)
			require.Equal(t, st.Output(), inputs["dockerfile"].Output())

			want, err := subdir(st, "app").Marshal(t.Context())
			require.NoError(t, err)
			got, err := inputs["context"].Marshal(t.Context())
			require.NoError(t, err)
			require.Equal(t, want.Def, got.Def)
		})
	}
}

func TestApplyBuildOptsDockerfileSource(t *testing.T) {
	st := llb.Git("https://example.com/repo.git", "")
	dockerfile := llb.Git("https://example.com/docker.git", "")
	attrs := map[string]string{}
	inputs := map[string]llb.State{}

	err := applyBuildOpts(attrs, map[string]string{}, inputs, new([]session.Attachable), BuildOpts{Rootfs: FSOpts{
		Source:           StateSource{State: st},
		Path:             "app",
		Dockerfile:       "Dockerfile.prod",
		DockerfileSource: StateSource{State: dockerfile},
	}})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"filename": "Dockerfile.prod"}, attrs)
	require.Equal(t, dockerfile.Output(), inputs["dockerfile"].Output())

	want, err := subdir(st, "app").Marshal(t.Context())
	require.NoError(t, err)
	got, err := inputs["context"].Marshal(t.Context())
	require.NoError(t, err)
	require.Equal(t, want.Def, got.Def)
}

func TestApplyBuildOptsSessionSource(t *testing.T) {
	git := llb.Git("https://example.com/repo.git", "")

	for _, tc := range []struct {
		name       string
		rootfs     FSOpts
		filename   string
		dockerfile llb.State
		context    *llb.State // nil when the frontend reads its local context
	}{
		{
			name:       "root of context",
			rootfs:     FSOpts{Source: SessionSource{Name: "context"}, Path: "Dockerfile"},
			filename:   "Dockerfile",
			dockerfile: llb.Local("context", llb.FollowPaths([]string{"Dockerfile", "Dockerfile.dockerignore"}), llb.Differ(llb.DiffNone, false), llb.SharedKeyHint("context:Dockerfile:Dockerfile.dockerignore")),
		},
		{
			name:       "root of another name",
			rootfs:     FSOpts{Source: SessionSource{Name: "app"}, Path: "Dockerfile"},
			filename:   "Dockerfile",
			dockerfile: llb.Local("app", llb.FollowPaths([]string{"Dockerfile", "Dockerfile.dockerignore"}), llb.Differ(llb.DiffNone, false), llb.SharedKeyHint("app:Dockerfile:Dockerfile.dockerignore")),
			context:    new(llb.Local("app", llb.SharedKeyHint("app:."))),
		},
		{
			name:       "subdir",
			rootfs:     FSOpts{Source: SessionSource{Name: "context"}, Path: "app/Dockerfile.prod"},
			filename:   "app/Dockerfile.prod",
			dockerfile: llb.Local("context", llb.FollowPaths([]string{"app/Dockerfile.prod", "app/Dockerfile.prod.dockerignore"}), llb.Differ(llb.DiffNone, false), llb.SharedKeyHint("context:app/Dockerfile.prod:app/Dockerfile.prod.dockerignore")),
			context:    new(subdir(llb.Local("context", llb.FollowPaths([]string{"app"}), llb.SharedKeyHint("context:app")), "app")),
		},
		{
			name:       "session dockerfile source",
			rootfs:     FSOpts{Source: StateSource{State: git}, Path: "app", Dockerfile: "Dockerfile.prod", DockerfileSource: SessionSource{Name: "dockerfile"}},
			filename:   "Dockerfile.prod",
			dockerfile: llb.Local("dockerfile", llb.FollowPaths([]string{"Dockerfile.prod", "Dockerfile.prod.dockerignore"}), llb.Differ(llb.DiffNone, false), llb.SharedKeyHint("dockerfile:Dockerfile.prod:Dockerfile.prod.dockerignore")),
			context:    new(subdir(git, "app")),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attrs := map[string]string{}
			localDirs := map[string]string{}
			inputs := map[string]llb.State{}

			require.NoError(t, resolveSource(&tc.rootfs))
			err := applyBuildOpts(attrs, localDirs, inputs, new([]session.Attachable), BuildOpts{Rootfs: tc.rootfs})
			require.NoError(t, err)
			require.Equal(t, map[string]string{"filename": tc.filename}, attrs)
			require.Empty(t, localDirs)
			requireSameState(t, tc.dockerfile, inputs["dockerfile"])
			if tc.context == nil {
				require.NotContains(t, inputs, "context")
				return
			}
			requireSameState(t, *tc.context, inputs["context"])
		})
	}
}

// requireSameState asserts that want and got marshal to the same LLB. A local
// source gets a random ID on each marshal, thus both use the same one.
func requireSameState(t *testing.T, want, got llb.State) {
	t.Helper()
	wantDef, err := want.Marshal(t.Context(), llb.LocalUniqueID("test"))
	require.NoError(t, err)
	gotDef, err := got.Marshal(t.Context(), llb.LocalUniqueID("test"))
	require.NoError(t, err)
	require.Equal(t, wantDef.Def, gotDef.Def)
}

func TestApplyBuildOptsContexts(t *testing.T) {
	st := llb.Git("https://example.com/repo.git", "")
	files := llb.Git("https://example.com/files.git", "")
	rootfs := FSOpts{Source: StateSource{State: st}, Path: "Dockerfile"}

	t.Run("state and local", func(t *testing.T) {
		attrs := map[string]string{}
		localDirs := map[string]string{}
		inputs := map[string]llb.State{}

		err := applyBuildOpts(attrs, localDirs, inputs, new([]session.Attachable), BuildOpts{
			Rootfs: rootfs,
			Contexts: map[string]Source{
				"files": StateSource{State: files},
				"base":  LocalSource{Dir: "/src/base"},
			},
		})
		require.NoError(t, err)
		require.Equal(t, "input:files", attrs["context:files"])
		require.Equal(t, files.Output(), inputs["files"].Output())
		require.Equal(t, "local:base", attrs["context:base"])
		require.Equal(t, map[string]string{"base": "/src/base"}, localDirs)
	})

	t.Run("session", func(t *testing.T) {
		attrs := map[string]string{}
		localDirs := map[string]string{}
		inputs := map[string]llb.State{}

		err := applyBuildOpts(attrs, localDirs, inputs, new([]session.Attachable), BuildOpts{
			Rootfs:   rootfs,
			Contexts: map[string]Source{"files": SessionSource{Name: "shared"}},
		})
		require.NoError(t, err)
		require.Equal(t, "local:shared", attrs["context:files"])
		require.Empty(t, localDirs)
		require.NotContains(t, inputs, "files")
	})

	t.Run("local and session", func(t *testing.T) {
		err := applyBuildOpts(map[string]string{}, map[string]string{}, map[string]llb.State{}, new([]session.Attachable), BuildOpts{
			Rootfs: rootfs,
			Contexts: map[string]Source{
				"files": SessionSource{Name: "files"},
				"base":  LocalSource{Dir: "/src/base"},
			},
		})
		require.ErrorContains(t, err, "cannot mix local and session sources")
	})

	t.Run("session without name", func(t *testing.T) {
		err := applyBuildOpts(map[string]string{}, map[string]string{}, map[string]llb.State{}, new([]session.Attachable), BuildOpts{
			Rootfs:   rootfs,
			Contexts: map[string]Source{"files": SessionSource{}},
		})
		require.ErrorContains(t, err, "needs a name")
	})

	t.Run("an image name is familiar, without the latest tag", func(t *testing.T) {
		attrs := map[string]string{}
		inputs := map[string]llb.State{}

		err := applyBuildOpts(attrs, map[string]string{}, inputs, new([]session.Attachable), BuildOpts{
			Rootfs: rootfs,
			Contexts: map[string]Source{
				"docker.io/library/alpine:latest": StateSource{State: files},
				"ghcr.io/acme/base:v1":            StateSource{State: st},
			},
		})
		require.NoError(t, err)
		require.Equal(t, "input:alpine", attrs["context:alpine"])
		require.Equal(t, files.Output(), inputs["alpine"].Output())
		require.Equal(t, "input:ghcr.io/acme/base:v1", attrs["context:ghcr.io/acme/base:v1"])
	})

	t.Run("two names of one context", func(t *testing.T) {
		err := applyBuildOpts(map[string]string{}, map[string]string{}, map[string]llb.State{}, new([]session.Attachable), BuildOpts{
			Rootfs: rootfs,
			Contexts: map[string]Source{
				"alpine":        StateSource{State: files},
				"alpine:latest": StateSource{State: st},
			},
		})
		require.ErrorContains(t, err, "two contexts")
	})

	for _, name := range []string{"", "Build", "context", "dockerfile"} {
		t.Run("invalid "+name, func(t *testing.T) {
			err := applyBuildOpts(map[string]string{}, map[string]string{}, map[string]llb.State{}, new([]session.Attachable), BuildOpts{
				Rootfs:   rootfs,
				Contexts: map[string]Source{name: StateSource{State: files}},
			})
			require.ErrorContains(t, err, "invalid context name")
		})
	}
}
