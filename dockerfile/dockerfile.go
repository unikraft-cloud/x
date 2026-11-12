// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package dockerfile reads the images that a Dockerfile builds from.
//
// It resolves them as dockerfile2llb does, so a Dockerfile of one FROM line
// can carry an image for Dependabot to bump, and the code that uses it can
// embed the file and read the image back.
package dockerfile

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/distribution/reference"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/dockerfile/shell"
)

const scratch = "scratch"

// Images returns the images that the stages of dt build from, in order, as
// normalized references such as docker.io/library/alpine:latest. A stage
// from scratch or from an earlier stage has no image. An ARG before the
// first FROM expands to its default.
func Images(dt []byte) ([]string, error) {
	res, err := parser.Parse(bytes.NewReader(dt))
	if err != nil {
		return nil, err
	}
	stages, metaArgs, err := instructions.Parse(res.AST, nil)
	if err != nil {
		return nil, err
	}

	lex := shell.NewLex(res.EscapeToken)
	globals := args{}
	for _, cmd := range metaArgs {
		for _, kv := range cmd.Args {
			if kv.Value == nil {
				continue
			}
			v, err := lex.ProcessWordWithMatches(*kv.Value, globals)
			if err != nil {
				return nil, fmt.Errorf("expanding ARG %s: %w", kv.Key, err)
			}
			globals[kv.Key] = v.Result
		}
	}

	var images []string
	named := map[string]bool{}
	for _, st := range stages {
		base, err := lex.ProcessWordWithMatches(st.BaseName, globals)
		if err != nil {
			return nil, fmt.Errorf("expanding %s: %w", st.OrigCmd, err)
		}
		if base.Result == "" {
			return nil, fmt.Errorf("base name (%s) should not be blank", st.BaseName)
		}
		// Like dockerfile2llb, stage names are lowercased but the base isn't.
		if base.Result != scratch && !named[base.Result] {
			ref, err := reference.ParseNormalizedNamed(base.Result)
			if err != nil {
				return nil, fmt.Errorf("parsing %q: %w", base.Result, err)
			}
			images = append(images, reference.TagNameOnly(ref).String())
		}
		if st.Name != "" {
			named[strings.ToLower(st.Name)] = true
		}
	}
	return images, nil
}

// Image returns the only image that dt builds from.
func Image(dt []byte) (string, error) {
	images, err := Images(dt)
	if err != nil {
		return "", err
	}
	if len(images) != 1 {
		return "", fmt.Errorf("want one image, got %d: %v", len(images), images)
	}
	return images[0], nil
}

// MustImage is like Image, but panics on an error. It's for a Dockerfile
// embedded with go:embed, where an error is a bug.
func MustImage(dt []byte) string {
	image, err := Image(dt)
	if err != nil {
		panic(err)
	}
	return image
}

type args map[string]string

func (a args) Get(key string) (string, bool) {
	v, ok := a[key]
	return v, ok
}

func (a args) Keys() []string {
	return slices.Sorted(maps.Keys(a))
}
