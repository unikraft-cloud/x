// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"context"
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/moby/buildkit/client/llb"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"unikraft.com/x/kraftfile"
)

// Source is the directory tree that the path of a rootfs or a rom resolves in.
type Source interface {
	isSource()
}

// LocalSource is a directory on the host.
type LocalSource struct {
	Dir string
}

// StateSource is a directory tree that BuildKit solves from LLB. The build
// does not apply a .dockerignore file to it.
type StateSource struct {
	State llb.State
}

// FrontendSource is a reference resolved by the Dockerfile frontend.
// It is intended for named build contexts, not rootfs or ROM filesystem paths.
type FrontendSource struct {
	Ref string
}

// SessionSource is a directory with root owners that the session serves by Name.
// A main Dockerfile context uses .dockerignore only at the root of "context".
type SessionSource struct {
	Name string
}

func (LocalSource) isSource() {}

func (StateSource) isSource() {}

func (SessionSource) isSource() {}

func (FrontendSource) isSource() {}

// fromLLB reports whether BuildKit gets src as LLB, and not from the host.
func fromLLB(src Source) bool {
	switch src.(type) {
	case StateSource, SessionSource:
		return true
	}
	return false
}

// hasSession reports whether the rootfs of opts or one of its contexts is a
// SessionSource.
func hasSession(opts BuildOpts) bool {
	srcs := append([]Source{opts.Rootfs.Source, opts.Rootfs.DockerfileSource}, slices.Collect(maps.Values(opts.Contexts))...)
	return slices.ContainsFunc(srcs, func(src Source) bool {
		_, ok := src.(SessionSource)
		return ok
	})
}

// sourceState returns the LLB of paths in src, which must come from LLB. A
// SessionSource sends only paths with opts, and a shared key that no other sync uses.
func sourceState(src Source, paths []string, opts ...llb.LocalOption) llb.State {
	s, ok := src.(SessionSource)
	if !ok {
		return src.(StateSource).State
	}
	opts = append(opts, llb.SharedKeyHint(s.Name+":"+strings.Join(paths, ":")))
	if !slices.Equal(paths, []string{"."}) {
		opts = append(opts, llb.FollowPaths(paths))
	}
	return llb.Local(s.Name, opts...)
}

// join returns p as a host path, resolving a relative p in s.Dir.
func (s LocalSource) join(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(s.Dir, p)
}

// localize turns a LocalSource into a host path and rejects an unknown
// Source. An OCI path names an image, thus it drops its Source.
func localize(o *FSOpts) error {
	if o.DockerfileSource != nil && (!fromLLB(o.Source) || !fromLLB(o.DockerfileSource)) {
		return fmt.Errorf("a dockerfile source and its source must both be LLB states or sessions")
	}
	for _, src := range []Source{o.Source, o.DockerfileSource} {
		if s, ok := src.(SessionSource); ok && s.Name == "" {
			return fmt.Errorf("a session source needs a name")
		}
	}

	switch src := o.Source.(type) {
	case nil, StateSource, SessionSource:
	case LocalSource:
		if o.Type != kraftfile.SourceTypeOCI {
			o.Path = src.join(o.Path)
		}
		o.Source = nil
	default:
		return fmt.Errorf("unsupported source %T", o.Source)
	}

	if o.Type == kraftfile.SourceTypeOCI {
		o.Source = nil
	}
	return nil
}

// resolveState cleans the path in a StateSource or a SessionSource and makes
// sure that its type can come from LLB.
func resolveState(o *FSOpts) error {
	p, err := cleanSourcePath(o.Path)
	if err != nil {
		return err
	}
	o.Path = p

	switch {
	case o.DockerfileSource != nil:
		if o.Dockerfile == "" {
			return fmt.Errorf("a dockerfile source needs a dockerfile")
		}
		if _, err := cleanSourcePath(o.Dockerfile); err != nil {
			return err
		}
	case o.Dockerfile != "":
		if _, err := cleanSourcePath(path.Join(o.Path, o.Dockerfile)); err != nil {
			return err
		}
	}
	if err := checkSessionPaths(o); err != nil {
		return err
	}

	switch o.Type {
	case kraftfile.SourceTypeDirectory, kraftfile.SourceTypeDockerfile:
		return nil
	}
	return fmt.Errorf("source type %q cannot come from %s", o.Type, sourceKind(o.Source))
}

// checkSessionPaths rejects a path that a session sends if it has a character
// that BuildKit reads as a pattern.
func checkSessionPaths(o *FSOpts) error {
	var paths []string
	if _, ok := o.Source.(SessionSource); ok {
		paths = append(paths, o.Path)
		if o.DockerfileSource == nil {
			paths = append(paths, path.Join(o.Path, o.Dockerfile))
		}
	}
	if _, ok := o.DockerfileSource.(SessionSource); ok {
		paths = append(paths, path.Clean(o.Dockerfile))
	}
	for _, p := range paths {
		if strings.ContainsAny(p, `*?[\`) || strings.HasPrefix(p, "!") {
			return fmt.Errorf(`path %q in a session cannot start with ! or contain *, ?, [ or \`, p)
		}
	}
	return nil
}

// sourceKind names the kind of src, which must come from LLB, for an error.
func sourceKind(src Source) string {
	if _, ok := src.(SessionSource); ok {
		return "a session"
	}
	return "an LLB state"
}

// cleanSourcePath returns p as a clean path in a StateSource or a
// SessionSource, with "." for its root.
func cleanSourcePath(p string) (string, error) {
	p = path.Clean(p)
	switch {
	case path.IsAbs(p):
		return "", fmt.Errorf("path %q must be relative to the source", p)
	case p == ".." || strings.HasPrefix(p, "../"):
		return "", fmt.Errorf("path %q is outside the source", p)
	}
	return p, nil
}

// subdir returns the directory p of st as the root of a new state. When p is
// a symlink, the state holds the directory that it points to.
func subdir(st llb.State, p string) llb.State {
	if p == "." {
		return st
	}
	return llb.Scratch().File(llb.Copy(st, "/"+p, "/", &llb.CopyInfo{
		CopyDirContentsOnly: true,
		FollowSymlinks:      true,
	}))
}

// marshalState marshals st for BuildKit to solve on platform p.
func marshalState(ctx context.Context, st llb.State, p ocispec.Platform, noCache bool) (*llb.Definition, error) {
	constraints := []llb.ConstraintsOpt{llb.Platform(p)}
	if noCache {
		constraints = append(constraints, llb.IgnoreCache)
	}
	return st.Marshal(ctx, constraints...)
}
