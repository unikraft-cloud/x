// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
)

// gitRef holds the parsed components of a Git repository reference.
//
// Supported forms:
//
//	host/org/repo@ref#file=path   (single file)
//	host/org/repo@ref#dir=path    (directory)
type gitRef struct {
	host string
	org  string
	repo string
	ref  string
	file string
	dir  string
}

func (g gitRef) sshURL() string {
	return fmt.Sprintf("git@%s:%s/%s.git", g.host, g.org, g.repo)
}

func (g gitRef) httpsURL() string {
	return fmt.Sprintf("https://%s/%s/%s.git", g.host, g.org, g.repo)
}

// parseGitRef parses a Git repository reference.  It recognises both
// #file=path and #dir=path fragments.  Returns nil when s does not match.
func parseGitRef(s string) *gitRef {
	atIdx := strings.Index(s, "@")
	if atIdx < 0 {
		return nil
	}

	var file, dir string
	var hashIdx int
	switch {
	case strings.Contains(s, "#file="):
		hashIdx = strings.Index(s, "#file=")
		file = s[hashIdx+len("#file="):]
	case strings.Contains(s, "#dir="):
		hashIdx = strings.Index(s, "#dir=")
		dir = s[hashIdx+len("#dir="):]
	default:
		return nil
	}

	hostOrgRepo := s[:atIdx]
	ref := s[atIdx+1 : hashIdx]

	parts := strings.SplitN(hostOrgRepo, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil
	}

	if ref == "" || (file == "" && dir == "") {
		return nil
	}

	return &gitRef{
		host: parts[0],
		org:  parts[1],
		repo: parts[2],
		ref:  ref,
		file: file,
		dir:  dir,
	}
}

// cloneGitRepo performs a shallow clone of the repository described by g.
// It tries SSH (via ssh-agent) first, falling back to unauthenticated
// HTTPS.  The caller is responsible for removing the returned directory
// when it is no longer needed.
func cloneGitRepo(g *gitRef) (string, error) {
	tmpDir, err := os.MkdirTemp("", "openapi-gen-git-*")
	if err != nil {
		return "", fmt.Errorf("creating temp directory: %w", err)
	}

	refName := plumbing.NewBranchReferenceName(g.ref)

	type attempt struct {
		url  string
		opts *git.CloneOptions
	}

	sshAuth, sshErr := gitssh.NewSSHAgentAuth("git")

	attempts := []attempt{}
	if sshErr == nil {
		attempts = append(attempts, attempt{
			url: g.sshURL(),
			opts: &git.CloneOptions{
				URL:           g.sshURL(),
				Auth:          sshAuth,
				ReferenceName: refName,
				SingleBranch:  true,
				Depth:         1,
				Tags:          git.NoTags,
			},
		})
	}
	attempts = append(attempts, attempt{
		url: g.httpsURL(),
		opts: &git.CloneOptions{
			URL:           g.httpsURL(),
			ReferenceName: refName,
			SingleBranch:  true,
			Depth:         1,
			Tags:          git.NoTags,
		},
	})

	var cloneErr error
	for _, a := range attempts {
		_, err := git.PlainClone(tmpDir, false, a.opts)
		if err != nil {
			cloneErr = fmt.Errorf("cloning %s: %w", a.url, err)
			os.RemoveAll(tmpDir)
			if err := os.MkdirAll(tmpDir, 0o755); err != nil {
				return "", fmt.Errorf("recreating temp directory: %w", err)
			}
			continue
		}
		cloneErr = nil
		break
	}
	if cloneErr != nil {
		os.RemoveAll(tmpDir)
		return "", cloneErr
	}

	return tmpDir, nil
}

// readSpecFromGit clones the repository and returns the contents of the
// file specified by g.file.
func readSpecFromGit(g *gitRef) ([]byte, error) {
	tmpDir, err := cloneGitRepo(g)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	data, err := os.ReadFile(filepath.Join(tmpDir, g.file))
	if err != nil {
		return nil, fmt.Errorf("reading %s from cloned repository: %w", g.file, err)
	}

	return data, nil
}

// resolveTemplateDirFromGit clones the repository and returns the absolute
// path to the directory specified by g.dir inside the clone.  The caller
// must call the returned cleanup function when the directory is no longer
// needed.
func resolveTemplateDirFromGit(g *gitRef) (dir string, cleanup func(), err error) {
	tmpDir, err := cloneGitRepo(g)
	if err != nil {
		return "", nil, err
	}

	resolved := filepath.Join(tmpDir, g.dir)
	info, err := os.Stat(resolved)
	if err != nil {
		os.RemoveAll(tmpDir)
		return "", nil, fmt.Errorf("accessing %s in cloned repository: %w", g.dir, err)
	}
	if !info.IsDir() {
		os.RemoveAll(tmpDir)
		return "", nil, fmt.Errorf("%s is not a directory in the cloned repository", g.dir)
	}

	return resolved, func() { os.RemoveAll(tmpDir) }, nil
}
