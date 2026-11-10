// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

//go:build integration

package build_test

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/platforms"
	"github.com/docker/cli/cli/config"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"unikraft.com/x/buildkit"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/integration"
	"unikraft.com/x/kraftfile"
	"unikraft.com/x/log"

	"unikraft.com/x/build"
	"unikraft.com/x/build/buildflags"
	"unikraft.com/x/build/registryauth"
)

func TestBuildSinglePlatformIntegration(t *testing.T) {
	ctx := buildIntegrationContext(t)
	dockerfile := `
FROM scratch

COPY <<'EOF' /hello.txt
hello
EOF
`
	rootfsPath := writeBuildDockerfile(t, dockerfile)
	opts := build.BuildOpts{
		Runtime: "unikraft.io/official/base-compat",
		Rootfs: build.FSOpts{
			Format: kraftfile.FsTypeCpio,
			Type:   kraftfile.SourceTypeDockerfile,
			Path:   rootfsPath,
		},
		Platform: []ocispec.Platform{{OS: "kraftcloud", Architecture: "x86_64"}},
	}

	imgs := runIntegrationBuild(t, ctx, opts)
	require.Len(t, imgs, 1)
	assertBuildPlatforms(t, imgs, []string{"kraftcloud/x86_64"})
}

func TestBuildSinglePlatformErofsIntegration(t *testing.T) {
	ctx := buildIntegrationContext(t)
	dockerfile := `
FROM scratch

COPY <<'EOF' /hello.txt
hello
EOF
`
	rootfsPath := writeBuildDockerfile(t, dockerfile)
	opts := build.BuildOpts{
		Runtime: "unikraft.io/official/base-compat",
		Rootfs: build.FSOpts{
			Format: kraftfile.FsTypeErofs,
			Type:   kraftfile.SourceTypeDockerfile,
			Path:   rootfsPath,
		},
		Platform: []ocispec.Platform{{OS: "kraftcloud", Architecture: "x86_64"}},
	}

	imgs := runIntegrationBuild(t, ctx, opts)
	require.Len(t, imgs, 1)
	assertBuildPlatforms(t, imgs, []string{"kraftcloud/x86_64"})
}

func TestBuildDefaultErofsFormatBaseCompatIntegration(t *testing.T) {
	ctx := buildIntegrationContext(t)
	dockerfile := `
FROM scratch

COPY <<'EOF' /hello.txt
hello
EOF
`
	rootfsPath := writeBuildDockerfile(t, dockerfile)
	opts := build.BuildOpts{
		Runtime: "unikraft.io/official/base-compat",
		Rootfs: build.FSOpts{
			Type: kraftfile.SourceTypeDockerfile,
			Path: rootfsPath,
		},
		Platform: []ocispec.Platform{{OS: "kraftcloud", Architecture: "x86_64"}},
	}

	imgs := runIntegrationBuild(t, ctx, opts)
	require.Len(t, imgs, 1)
	assertBuildPlatforms(t, imgs, []string{"kraftcloud/x86_64"})

	files := integration.ReadErofsInitrd(t, imgs[0])
	require.Contains(t, files, "hello.txt")
	require.Equal(t, "hello\n", files["hello.txt"])
}

func TestBuildAbsoluteSymlinksIntegration(t *testing.T) {
	ctx := buildIntegrationContext(t)
	// Reproducer for https://github.com/moby/buildkit/issues/6684
	// This test ensures that absolute symlinks are correctly preserved in the rootfs
	// and not incorrectly rewritten with platform-specific prefixes.
	// The debian:bookworm-slim image contains absolute symlinks like /etc/alternatives/awk -> /usr/bin/mawk
	dockerfile := `
FROM ` + integration.Image + `

# Create test symlinks to verify correct behavior
RUN ln -s /usr/bin/bash /test-symlink
RUN ln -s /etc/passwd /another-test-link
`
	rootfsPath := writeBuildDockerfile(t, dockerfile)
	opts := build.BuildOpts{
		Runtime: "unikraft.io/official/base-compat",
		Rootfs: build.FSOpts{
			Format: kraftfile.FsTypeCpio,
			Type:   kraftfile.SourceTypeDockerfile,
			Path:   rootfsPath,
		},
		Platform: []ocispec.Platform{{OS: "kraftcloud", Architecture: "x86_64"}},
	}

	imgs := runIntegrationBuild(t, ctx, opts)
	require.Len(t, imgs, 1)
	assertBuildPlatforms(t, imgs, []string{"kraftcloud/x86_64"})

	// Inspect the CPIO archive to verify symlink targets
	fsys := integration.OpenInitrd(t, imgs[0].Initrd, kraftfile.FsTypeCpio)

	// Track found symlinks and their targets
	foundSymlinks := make(map[string]string)
	require.NoError(t, fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink == 0 {
			return err
		}
		target, err := fs.ReadLink(fsys, path)
		foundSymlinks[path] = target
		return err
	}))

	// Verify our test symlinks are correct
	require.Contains(t, foundSymlinks, "test-symlink")
	require.Equal(t, "/usr/bin/bash", foundSymlinks["test-symlink"],
		"test-symlink should point to /usr/bin/bash, not a rewritten path")

	require.Contains(t, foundSymlinks, "another-test-link")
	require.Equal(t, "/etc/passwd", foundSymlinks["another-test-link"],
		"another-test-link should point to /etc/passwd, not a rewritten path")

	// Verify a symlink from the debian image itself
	require.Contains(t, foundSymlinks, "etc/alternatives/awk")
	require.Equal(t, "/usr/bin/mawk", foundSymlinks["etc/alternatives/awk"],
		"Debian's awk symlink should point to /usr/bin/mawk, not a rewritten path")

	// Ensure no symlinks have been incorrectly rewritten with platform prefixes
	for name, target := range foundSymlinks {
		require.NotContains(t, target, "fc_x86_64",
			"symlink %s should not contain platform prefix in target: %s", name, target)
		require.NotContains(t, target, "/fc_x86_64",
			"symlink %s should not contain platform prefix in target: %s", name, target)
	}
}

func TestBuildMultiPlatformIntegration(t *testing.T) {
	ctx := buildIntegrationContext(t)
	dockerfile := `
FROM scratch

COPY <<'EOF' /hello.txt
hello
EOF
`
	rootfsPath := writeBuildDockerfile(t, dockerfile)
	opts := build.BuildOpts{
		Runtime: "unikraft.io/unikraft.org/base",
		Rootfs: build.FSOpts{
			Format: kraftfile.FsTypeCpio,
			Type:   kraftfile.SourceTypeDockerfile,
			Path:   rootfsPath,
		},
		Platform: []ocispec.Platform{
			{OS: "fc", Architecture: "x86_64"},
			{OS: "qemu", Architecture: "x86_64"},
		},
	}

	imgs := runIntegrationBuild(t, ctx, opts)
	require.Len(t, imgs, 2)
	assertBuildPlatforms(t, imgs, []string{"fc/x86_64", "qemu/x86_64"})
}

func TestBuildSecretsIntegration(t *testing.T) {
	ctx := buildIntegrationContext(t)
	dockerfile := `
FROM ` + integration.Image + `
RUN --mount=type=secret,id=api_key cat /run/secrets/api_key | grep -q s3cr3t
`
	rootfsPath := writeBuildDockerfile(t, dockerfile)
	secretPath := writeBuildSecret(t, "s3cr3t\n")
	secrets, err := buildflags.ParseSecretSpecs([]string{"id=api_key,src=" + secretPath})
	require.NoError(t, err)

	opts := build.BuildOpts{
		Runtime: "unikraft.io/official/base-compat",
		Rootfs: build.FSOpts{
			Format: kraftfile.FsTypeCpio,
			Type:   kraftfile.SourceTypeDockerfile,
			Path:   rootfsPath,
		},
		Secrets:  secrets,
		Platform: []ocispec.Platform{{OS: "kraftcloud", Architecture: "x86_64"}},
	}
	imgs := runIntegrationBuild(t, ctx, opts)
	require.Len(t, imgs, 1)
	assertBuildPlatforms(t, imgs, []string{"kraftcloud/x86_64"})
}

func TestBuildCmdEnvLabelsIntegration(t *testing.T) {
	ctx := buildIntegrationContext(t)
	dockerfile := `
FROM scratch
LABEL com.example.source=dockerfile com.example.only=dockerfile
ENV DOCKERFILE_ENV=from-dockerfile
CMD ["/dockerfile-cmd"]
`
	rootfsPath := writeBuildDockerfile(t, dockerfile)
	opts := build.BuildOpts{
		Runtime: "unikraft.io/official/base-compat",
		Rootfs: build.FSOpts{
			Format: kraftfile.FsTypeCpio,
			Type:   kraftfile.SourceTypeDockerfile,
			Path:   rootfsPath,
		},
		Platform: []ocispec.Platform{{OS: "kraftcloud", Architecture: "x86_64"}},
		Cmd:      []string{"/unikraft-cmd", "--from-opts"},
		Env: kraftfile.Map{
			{Key: "OPTS_ENV", Value: "from-opts"},
			{Key: "OPTS_FLAG", Value: "1"},
		},
		Labels: map[string]string{
			"com.example.source": "opts",
			"com.example.extra":  "true",
		},
	}

	imgs := runIntegrationBuild(t, ctx, opts)
	require.Len(t, imgs, 1)
	require.NotNil(t, imgs[0])
	require.NotNil(t, imgs[0].Image)
	require.Equal(t, opts.Cmd, imgs[0].Image.Config.Cmd)
	require.Equal(t, opts.Labels, imgs[0].Image.Config.Labels)
	require.Contains(t, imgs[0].Image.Config.Env, "OPTS_ENV=from-opts")
	require.Contains(t, imgs[0].Image.Config.Env, "OPTS_FLAG=1")
	require.Contains(t, imgs[0].Image.Config.Env, "DOCKERFILE_ENV=from-dockerfile")
}

func TestBuildDockerfileLabelsIntegration(t *testing.T) {
	ctx := buildIntegrationContext(t)
	dockerfile := `
FROM scratch
LABEL com.example.source=dockerfile com.example.only=dockerfile
`
	imgs := runIntegrationBuild(t, ctx, build.BuildOpts{
		Runtime: "unikraft.io/official/base-compat",
		Rootfs: build.FSOpts{
			Format: kraftfile.FsTypeCpio,
			Type:   kraftfile.SourceTypeDockerfile,
			Path:   writeBuildDockerfile(t, dockerfile),
		},
		Platform: []ocispec.Platform{{OS: "kraftcloud", Architecture: "x86_64"}},
	})
	require.Len(t, imgs, 1)
	require.NotNil(t, imgs[0])
	require.NotNil(t, imgs[0].Image)
	require.Equal(t, "dockerfile", imgs[0].Image.Config.Labels["com.example.source"])
	require.Equal(t, "dockerfile", imgs[0].Image.Config.Labels["com.example.only"])
}

func buildIntegrationContext(t *testing.T) context.Context {
	t.Helper()
	integration.Token(t)

	ctx := t.Context()
	logger, err := log.New(ctx, log.Config{Sink: t.Output(), Type: log.TextType, Level: log.InfoLevel})
	require.NoError(t, err)
	return log.WithLogger(ctx, logger)
}

// tokenCredentials gives the registry credentials of UKC_TOKEN.
func tokenCredentials(t *testing.T) registryauth.Credentials {
	t.Helper()
	creds, err := registryauth.FromToken(integration.Token(t))
	require.NoError(t, err)
	return creds
}

func tokenHosts(creds registryauth.Credentials) docker.RegistryHosts {
	return docker.ConfigureDefaultRegistries(docker.WithAuthorizer(creds.Authorizer()))
}

func tokenAccessor(creds registryauth.Credentials) *imagespec.Accessor {
	hosts := tokenHosts(creds)
	return imagespec.NewAccessor(
		imagespec.WithResolver(docker.NewResolver(docker.ResolverOptions{Hosts: hosts})),
		imagespec.WithRegistryHosts(hosts),
	)
}

func writeBuildDockerfile(t *testing.T, dockerfile string) string {
	t.Helper()
	dir := t.TempDir()
	dockerfilePath := filepath.Join(dir, "Dockerfile")
	require.NoError(t, os.WriteFile(dockerfilePath, []byte(dockerfile), 0o644))
	return dockerfilePath
}

func writeBuildSecret(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

func runIntegrationBuild(t *testing.T, ctx context.Context, opts build.BuildOpts) []*imagespec.Image {
	t.Helper()
	imgs, err := integrationBuilder(t, ctx).Build(ctx, opts)
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, img := range imgs {
			_ = img.Close()
		}
	})

	return imgs
}

// runIntegrationRootfs calls BuildRootfs with the builder of integrationBuilder.
func runIntegrationRootfs(t *testing.T, ctx context.Context, opts build.BuildOpts) []*imagespec.Image {
	t.Helper()
	imgs, err := integrationBuilder(t, ctx).BuildRootfs(ctx, opts)
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, img := range imgs {
			_ = img.Close()
		}
	})
	return imgs
}

// localBuildkit is the buildkitd that the tests build on.
var localBuildkit = buildkit.Fallback{buildkit.Host{}, buildkit.Docker{}}

// integrationBuilder gives a builder on one BuildKit connection that logs in
// to Unikraft Cloud's registry with UKC_TOKEN, and skips the test when no
// daemon answers.
func integrationBuilder(t *testing.T, ctx context.Context) build.Builder {
	t.Helper()
	creds := tokenCredentials(t)
	d, err := localBuildkit.Acquire(ctx, buildkit.Key{})
	if err != nil {
		t.Skipf("buildkit unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Release() })

	return build.Builder{
		Images: func(context.Context) (*imagespec.Accessor, error) {
			return tokenAccessor(creds), nil
		},
		Buildkit: func(context.Context) (*client.Client, func(), error) {
			return d.Client, nil, nil
		},
		Session: func(context.Context) ([]session.Attachable, error) {
			return []session.Attachable{creds.Attachable()}, nil
		},
	}
}

// mirrorHelloWorld copies integration.HelloWorld into the organization of
// UKC_TOKEN, and gives the reference of the copy. TestMain deletes it.
func mirrorHelloWorld(t *testing.T, ctx context.Context) string {
	t.Helper()
	creds := tokenCredentials(t)
	// The user of a token is robot$<organization>.users.kraftcloud.
	user, _, err := creds("unikraft.io")
	require.NoError(t, err)
	org := strings.TrimSuffix(strings.TrimPrefix(user, "robot$"), ".users.kraftcloud")
	dockerCreds := registryauth.FromDockerConfig(config.LoadDefaultConfigFile(io.Discard))
	resolver := docker.NewResolver(docker.ResolverOptions{Hosts: tokenHosts(creds.Or(dockerCreds))})
	return integration.HelloWorld.Mirror(t, ctx, resolver, "unikraft.io/"+org)
}

func TestMain(m *testing.M) {
	m.Run()
	ref := integration.HelloWorld.Ref()
	if ref == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	creds, err := registryauth.FromToken(os.Getenv("UKC_TOKEN"))
	var loc *imagespec.Location
	if err == nil {
		loc, err = imagespec.GuessLocation(ref)
	}
	if err == nil {
		err = tokenAccessor(creds).Delete(ctx, loc)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to delete %s: %v\n", ref, err)
	}
}

func assertBuildPlatforms(t *testing.T, imgs []*imagespec.Image, expected []string) {
	t.Helper()
	platformsFound := make([]string, 0, len(imgs))
	for _, img := range imgs {
		require.NotNil(t, img)
		require.NotNil(t, img.Image)
		platformsFound = append(platformsFound, platforms.Format(img.Image.Platform))
	}
	require.ElementsMatch(t, expected, platformsFound)
}

func TestBuildCharDeviceErofsIntegration(t *testing.T) {
	ctx := buildIntegrationContext(t)
	dockerfile := `
FROM ` + integration.Image + `
RUN mknod /testfile c 220 220
`
	rootfsPath := writeBuildDockerfile(t, dockerfile)
	opts := build.BuildOpts{
		Runtime: "unikraft.io/official/base-compat",
		Rootfs: build.FSOpts{
			Format: kraftfile.FsTypeErofs,
			Type:   kraftfile.SourceTypeDockerfile,
			Path:   rootfsPath,
		},
		Platform: []ocispec.Platform{{OS: "kraftcloud", Architecture: "x86_64"}},
	}

	imgs := runIntegrationBuild(t, ctx, opts)
	require.Len(t, imgs, 1)
	assertBuildPlatforms(t, imgs, []string{"kraftcloud/x86_64"})

	erofsImg := integration.OpenErofsInitrdImage(t, imgs[0])
	inodes := integration.WalkErofsInodes(t, erofsImg)

	require.Contains(t, inodes, "testfile")
	ino := inodes["testfile"]
	require.True(t, ino.IsCharDev(), "testfile should be a character device node")
	require.Equal(t, uint32(220), ino.GetDevMajor(), "char device major number should be preserved")
	require.Equal(t, uint32(220), ino.GetDevMinor(), "char device minor number should be preserved")
}

func TestBuildHiddenFileErofsIntegration(t *testing.T) {
	ctx := buildIntegrationContext(t)
	dockerfile := `
FROM scratch
COPY <<'EOF' /.hidden
hidden content
EOF
`
	rootfsPath := writeBuildDockerfile(t, dockerfile)
	opts := build.BuildOpts{
		Runtime: "unikraft.io/official/base-compat",
		Rootfs: build.FSOpts{
			Format: kraftfile.FsTypeErofs,
			Type:   kraftfile.SourceTypeDockerfile,
			Path:   rootfsPath,
		},
		Platform: []ocispec.Platform{{OS: "kraftcloud", Architecture: "x86_64"}},
	}

	imgs := runIntegrationBuild(t, ctx, opts)
	require.Len(t, imgs, 1)
	assertBuildPlatforms(t, imgs, []string{"kraftcloud/x86_64"})

	files := integration.ReadErofsInitrd(t, imgs[0])
	require.Contains(t, files, ".hidden", "hidden file must be preserved with its leading dot")
	require.Equal(t, "hidden content\n", files[".hidden"])
}

func TestBuildCustomDockerfileNameViaPathIntegration(t *testing.T) {
	ctx := buildIntegrationContext(t)
	dockerfile := `
FROM scratch

COPY <<'EOF' /hello.txt
hello
EOF
`
	dir := t.TempDir()
	dockerfilePath := filepath.Join(dir, "MyDockerfile")
	require.NoError(t, os.WriteFile(dockerfilePath, []byte(dockerfile), 0o644))

	opts := build.BuildOpts{
		Runtime: "unikraft.io/official/base-compat",
		Rootfs: build.FSOpts{
			Format: kraftfile.FsTypeErofs,
			Type:   kraftfile.SourceTypeDockerfile,
			Path:   dockerfilePath,
		},
		Platform: []ocispec.Platform{{OS: "kraftcloud", Architecture: "x86_64"}},
	}

	imgs := runIntegrationBuild(t, ctx, opts)
	require.Len(t, imgs, 1)
	assertBuildPlatforms(t, imgs, []string{"kraftcloud/x86_64"})

	files := integration.ReadErofsInitrd(t, imgs[0])
	require.Contains(t, files, "hello.txt")
	require.Equal(t, "hello\n", files["hello.txt"])
}

func TestRootfsDockerfileCpioIntegration(t *testing.T) {
	ctx := buildIntegrationContext(t)
	dockerfile := `
FROM scratch

COPY <<'EOF' /hello.txt
hello
EOF
`
	rootfsPath := writeBuildDockerfile(t, dockerfile)
	imgs := runIntegrationRootfs(t, ctx, build.BuildOpts{
		Rootfs: build.FSOpts{
			Format: kraftfile.FsTypeCpio,
			Path:   rootfsPath,
			Type:   kraftfile.SourceTypeDockerfile,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.Len(t, imgs, 1)

	files := integration.ReadCpioInitrd(t, imgs[0])
	require.Contains(t, files, "hello.txt")
	require.Equal(t, "hello\n", files["hello.txt"])
}

func TestRootfsDockerfileLabelsIntegration(t *testing.T) {
	dockerfile := `
FROM scratch
LABEL com.example.source=dockerfile com.example.only=dockerfile
`

	t.Run("preserved when no labels are given", func(t *testing.T) {
		ctx := buildIntegrationContext(t)
		imgs := runIntegrationRootfs(t, ctx, build.BuildOpts{
			Rootfs: build.FSOpts{
				Format: kraftfile.FsTypeCpio,
				Path:   writeBuildDockerfile(t, dockerfile),
				Type:   kraftfile.SourceTypeDockerfile,
			},
			Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
		})
		require.Len(t, imgs, 1)
		require.Equal(t, "dockerfile", imgs[0].Image.Config.Labels["com.example.source"])
		require.Equal(t, "dockerfile", imgs[0].Image.Config.Labels["com.example.only"])
	})

	t.Run("replaced by the given labels", func(t *testing.T) {
		ctx := buildIntegrationContext(t)
		imgs := runIntegrationRootfs(t, ctx, build.BuildOpts{
			Rootfs: build.FSOpts{
				Format: kraftfile.FsTypeCpio,
				Path:   writeBuildDockerfile(t, dockerfile),
				Type:   kraftfile.SourceTypeDockerfile,
			},
			Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
			Labels:   map[string]string{"com.example.source": "opts"},
		})
		require.Len(t, imgs, 1)
		require.Equal(t, map[string]string{"com.example.source": "opts"}, imgs[0].Image.Config.Labels)
	})
}

func TestRootfsDockerfileErofsIntegration(t *testing.T) {
	ctx := buildIntegrationContext(t)
	dockerfile := `
FROM scratch

COPY <<'EOF' /hello.txt
hello
EOF
`
	rootfsPath := writeBuildDockerfile(t, dockerfile)
	imgs := runIntegrationRootfs(t, ctx, build.BuildOpts{
		Rootfs: build.FSOpts{
			Format: kraftfile.FsTypeErofs,
			Path:   rootfsPath,
			Type:   kraftfile.SourceTypeDockerfile,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.Len(t, imgs, 1)

	files := integration.ReadErofsInitrd(t, imgs[0])
	require.Contains(t, files, "hello.txt")
	require.Equal(t, "hello\n", files["hello.txt"])
}

func TestRootfsDockerfileCustomNameIntegration(t *testing.T) {
	ctx := buildIntegrationContext(t)
	dockerfile := `
FROM scratch

COPY <<'EOF' /hello.txt
hello
EOF
`
	contextDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(contextDir, "MyDockerfile"), []byte(dockerfile), 0o644))

	imgs := runIntegrationRootfs(t, ctx, build.BuildOpts{
		Rootfs: build.FSOpts{
			Format:     kraftfile.FsTypeCpio,
			Path:       contextDir,
			Dockerfile: "MyDockerfile",
			Type:       kraftfile.SourceTypeDockerfile,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.Len(t, imgs, 1)

	files := integration.ReadCpioInitrd(t, imgs[0])
	require.Contains(t, files, "hello.txt")
	require.Equal(t, "hello\n", files["hello.txt"])
}

func TestRootfsDockerfileWithDeviceNodeIntegration(t *testing.T) {
	ctx := buildIntegrationContext(t)
	// Build an image that contains a device node. The local exporter
	// used during solve must not fail with "failed to create device"
	// when unpacking the result. See TOOL-791.
	dockerfile := `
FROM ` + integration.Image + `
RUN mknod /testdevice c 1 3

COPY <<'EOF' /hello.txt
hello
EOF
`
	rootfsPath := writeBuildDockerfile(t, dockerfile)

	for _, format := range []kraftfile.FsType{kraftfile.FsTypeCpio, kraftfile.FsTypeErofs} {
		t.Run(string(format), func(t *testing.T) {
			imgs := runIntegrationRootfs(t, ctx, build.BuildOpts{
				Rootfs: build.FSOpts{
					Format: format,
					Path:   rootfsPath,
					Type:   kraftfile.SourceTypeDockerfile,
				},
				Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
			})
			require.Len(t, imgs, 1)

			switch format {
			case kraftfile.FsTypeCpio:
				files := integration.ReadCpioInitrd(t, imgs[0])
				require.Contains(t, files, "hello.txt")
				require.Equal(t, "hello\n", files["hello.txt"])
			case kraftfile.FsTypeErofs:
				files := integration.ReadErofsInitrd(t, imgs[0])
				require.Contains(t, files, "hello.txt")
				require.Equal(t, "hello\n", files["hello.txt"])
			}
		})
	}
}

func TestRootfsDockerfileKeepsOwnersIntegration(t *testing.T) {
	ctx := buildIntegrationContext(t)
	dockerfile := `
FROM scratch

COPY --chown=1000:1000 <<'EOF' /owned.txt
owned
EOF
`
	rootfsPath := writeBuildDockerfile(t, dockerfile)
	imgs := runIntegrationRootfs(t, ctx, build.BuildOpts{
		Rootfs: build.FSOpts{
			Format: kraftfile.FsTypeErofs,
			Path:   rootfsPath,
			Type:   kraftfile.SourceTypeDockerfile,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.Len(t, imgs, 1)

	owners := integration.ReadOwners(t, imgs[0].Initrd, kraftfile.FsTypeErofs)
	require.Equal(t, integration.Owner{UID: 1000, GID: 1000}, owners["owned.txt"])
}

func TestRootfsMultiPlatform(t *testing.T) {
	ctx := buildIntegrationContext(t)
	// $BUILDPLATFORM avoids emulation; TARGETARCH still varies per platform.
	dockerfile := `
FROM --platform=$BUILDPLATFORM ` + integration.Image + ` AS base
ARG TARGETARCH
RUN echo "$TARGETARCH" > /arch.txt

FROM scratch
COPY --from=base /arch.txt /arch.txt
`
	rootfsPath := writeBuildDockerfile(t, dockerfile)
	imgs := runIntegrationRootfs(t, ctx, build.BuildOpts{
		Rootfs: build.FSOpts{
			Format: kraftfile.FsTypeCpio,
			Path:   rootfsPath,
			Type:   kraftfile.SourceTypeDockerfile,
		},
		Platform: []ocispec.Platform{
			{OS: "fc", Architecture: "x86_64"},
			{OS: "fc", Architecture: "arm64"},
		},
	})
	require.Len(t, imgs, 2)

	// Each platform must produce distinct content, proving they were
	// solved and exported independently rather than merged into one export.
	content := make(map[string]string)
	for _, img := range imgs {
		files := integration.ReadCpioInitrd(t, img)
		require.Contains(t, files, "arch.txt")
		content[platforms.Format(img.Image.Platform)] = files["arch.txt"]
	}
	require.Equal(t, "amd64\n", content["fc/x86_64"])
	require.Equal(t, "arm64\n", content["fc/arm64"])
}

func TestRootfsMultiPlatformEmulation(t *testing.T) {
	t.Skip("QEMU emulation is not reliably available across CI runners")

	ctx := buildIntegrationContext(t)
	// Unlike above, RUN executes under the target platform here, requiring
	// QEMU emulation rather than just varying TARGETARCH.
	dockerfile := `
FROM ` + integration.Image + ` AS base
RUN uname -m > /arch.txt

FROM scratch
COPY --from=base /arch.txt /arch.txt
`
	rootfsPath := writeBuildDockerfile(t, dockerfile)
	imgs := runIntegrationRootfs(t, ctx, build.BuildOpts{
		Rootfs: build.FSOpts{
			Format: kraftfile.FsTypeCpio,
			Path:   rootfsPath,
			Type:   kraftfile.SourceTypeDockerfile,
		},
		Platform: []ocispec.Platform{
			{OS: "fc", Architecture: "x86_64"},
			{OS: "fc", Architecture: "arm64"},
		},
	})
	require.Len(t, imgs, 2)

	content := make(map[string]string)
	for _, img := range imgs {
		files := integration.ReadCpioInitrd(t, img)
		require.Contains(t, files, "arch.txt")
		content[platforms.Format(img.Image.Platform)] = files["arch.txt"]
	}
	require.Equal(t, "x86_64\n", content["fc/x86_64"])
	require.Equal(t, "aarch64\n", content["fc/arm64"])
}

// TestRootfsOCIRegularImageIntegration reads a regular OCI image (plain layers,
// no unikraft components) from a registry and verifies that BuildKit flattens
// the layers and that the result is re-packaged into the requested rootfs
// format.
func TestRootfsOCIRegularImageIntegration(t *testing.T) {
	ctx := buildIntegrationContext(t)
	ref := mirrorHelloWorld(t, ctx)

	for _, format := range []kraftfile.FsType{kraftfile.FsTypeCpio, kraftfile.FsTypeErofs} {
		t.Run(string(format), func(t *testing.T) {
			imgs := runIntegrationRootfs(t, ctx, build.BuildOpts{
				Rootfs: build.FSOpts{
					Path:   ref,
					Type:   kraftfile.SourceTypeOCI,
					Format: format,
				},
				Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
			})
			require.Len(t, imgs, 1)

			switch format {
			case kraftfile.FsTypeCpio:
				files := integration.ReadCpioInitrd(t, imgs[0])
				require.Contains(t, files, "hello")
			case kraftfile.FsTypeErofs:
				files := integration.ReadErofsInitrd(t, imgs[0])
				require.Contains(t, files, "hello")
			}
		})
	}
}

// TestRootfsOCIRegularImageNoCacheIntegration guards the options that carry
// --no-cache into a raw LLB solve, which does not see the frontend attribute the
// Dockerfile path uses.
func TestRootfsOCIRegularImageNoCacheIntegration(t *testing.T) {
	ctx := buildIntegrationContext(t)
	imgs := runIntegrationRootfs(t, ctx, build.BuildOpts{
		Rootfs: build.FSOpts{
			Path:   mirrorHelloWorld(t, ctx),
			Type:   kraftfile.SourceTypeOCI,
			Format: kraftfile.FsTypeCpio,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
		NoCache:  true,
	})
	require.Len(t, imgs, 1)
	require.Contains(t, integration.ReadCpioInitrd(t, imgs[0]), "hello")
}

// TestRootfsOCIRegularImagePerArchIntegration covers two platforms that resolve
// to different manifests of one multi-arch image: each must be flattened on its
// own rather than sharing the first one's filesystem.
func TestRootfsOCIRegularImagePerArchIntegration(t *testing.T) {
	ctx := buildIntegrationContext(t)
	imgs := runIntegrationRootfs(t, ctx, build.BuildOpts{
		Rootfs: build.FSOpts{
			Path:   mirrorHelloWorld(t, ctx),
			Type:   kraftfile.SourceTypeOCI,
			Format: kraftfile.FsTypeCpio,
		},
		Platform: []ocispec.Platform{
			{OS: "fc", Architecture: "x86_64"},
			{OS: "fc", Architecture: "arm64"},
		},
	})
	require.Len(t, imgs, 2)
	require.NotEqual(t, integration.ReadCpioInitrd(t, imgs[0]), integration.ReadCpioInitrd(t, imgs[1]),
		"each architecture must get its own flattened filesystem")
	assertBuildPlatforms(t, imgs, []string{"fc/x86_64", "fc/arm64"})
}

// TestRootfsOCIRegularImageSharedFlattenIntegration covers two unikraft
// platforms that normalise onto the same linux platform: they share one source
// image, so the flatten is done once and reused rather than solved per platform.
func TestRootfsOCIRegularImageSharedFlattenIntegration(t *testing.T) {
	ctx := buildIntegrationContext(t)
	imgs := runIntegrationRootfs(t, ctx, build.BuildOpts{
		Rootfs: build.FSOpts{
			Path:   mirrorHelloWorld(t, ctx),
			Type:   kraftfile.SourceTypeOCI,
			Format: kraftfile.FsTypeCpio,
		},
		Platform: []ocispec.Platform{
			{OS: "fc", Architecture: "x86_64"},
			{OS: "qemu", Architecture: "x86_64"},
		},
	})
	require.Len(t, imgs, 2)
	require.Equal(t, integration.ReadCpioInitrd(t, imgs[0]), integration.ReadCpioInitrd(t, imgs[1]))
	assertBuildPlatforms(t, imgs, []string{"fc/x86_64", "qemu/x86_64"})
}
