// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/containerd/platforms"
	distref "github.com/distribution/reference"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver/pb"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/tonistiigi/fsutil"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/image-spec/schemes"

	goerofs "github.com/unikraft/go-archivefs/erofs"
	gotar "github.com/unikraft/go-archivefs/tarfs"
	gocpio "github.com/unikraft/go-cpio"
	"unikraft.com/x/kraftfile"
	"unikraft.com/x/log"

	"unikraft.com/x/build/buildflags"
	"unikraft.com/x/build/internal/buildfs"
	"unikraft.com/x/build/progress"
)

// applyConfigOverrides layers the build options' Cmd, Env and Labels on top of
// base, which is the config of the image the rootfs was built from.
func applyConfigOverrides(base ocispec.ImageConfig, opts BuildOpts) ocispec.ImageConfig {
	cfg := base
	if opts.Cmd != nil {
		cfg.Cmd = opts.Cmd
	}
	if opts.Env != nil {
		env := slices.Clone(cfg.Env)
		for _, kv := range opts.Env {
			entry := fmt.Sprintf("%s=%s", kv.Key, kv.Value)
			i := slices.IndexFunc(env, func(e string) bool {
				name, _, _ := strings.Cut(e, "=")
				return name == kv.Key
			})
			if i < 0 {
				env = append(env, entry)
				continue
			}
			env[i] = entry
		}
		cfg.Env = env
	}
	if opts.Labels != nil {
		cfg.Labels = opts.Labels
	}
	return cfg
}

// isDockerfile reports whether the file name names a Dockerfile.
func isDockerfile(name string) bool {
	return slices.Contains(strings.Split(name, "."), defaultDockerfileName)
}

// DetectSourceType inspects path and returns the detected RootfsType.
func DetectSourceType(path string) (kraftfile.SourceType, error) {
	if path == "" {
		return "", fmt.Errorf("empty rootfs path")
	}

	if isDockerfile(filepath.Base(path)) {
		return kraftfile.SourceTypeDockerfile, nil
	}

	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("rootfs path does not exist: %w", err)
		}
		return "", fmt.Errorf("checking rootfs source %q: %w", path, err)
	}

	switch {
	case fi.IsDir():
		return kraftfile.SourceTypeDirectory, nil
	case fi.Mode().IsRegular(), fi.Mode()&os.ModeSymlink != 0:
		if format, err := detectPackagedFormat(path); err == nil {
			return kraftfile.SourceType(format), nil
		}
		if f, err := os.Open(path); err == nil {
			defer f.Close()
			r, err := buildfs.MaybeGunzip(f)
			if err != nil {
				return "", nil
			}
			if gotar.IsValid(r) {
				return kraftfile.SourceTypeTarball, nil
			}
		}
		return "", fmt.Errorf("could not detect file rootfs type %q", path)
	default:
		return "", fmt.Errorf("could not detect rootfs type %q", path)
	}
}

// detectPackagedFormat reports the rootfs format of an already-packaged file.
func detectPackagedFormat(path string) (kraftfile.FsType, error) {
	switch {
	case gocpio.IsValidPath(path):
		return kraftfile.FsTypeCpio, nil
	case goerofs.IsValidPath(path):
		return kraftfile.FsTypeErofs, nil
	default:
		return "", fmt.Errorf("could not detect rootfs format of %q", path)
	}
}

// resolveSource resolves the path of fsOpts in its Source and fills in the
// source type when it was not requested explicitly.
func resolveSource(fsOpts *FSOpts) error {
	if fsOpts.Type == kraftfile.SourceTypeOCI && fsOpts.Dockerfile != "" {
		return fmt.Errorf("a dockerfile cannot be set when the source type is %q", kraftfile.SourceTypeOCI)
	}

	if err := localize(fsOpts); err != nil {
		return err
	}
	if fsOpts.Type == kraftfile.SourceTypeOCI {
		return nil
	}

	if fsOpts.Dockerfile != "" {
		if fsOpts.Type != "" && fsOpts.Type != kraftfile.SourceTypeDockerfile {
			return fmt.Errorf("source type must be %q when a dockerfile is set, got %q", kraftfile.SourceTypeDockerfile, fsOpts.Type)
		}
		fsOpts.Type = kraftfile.SourceTypeDockerfile
	}

	// BuildKit cannot stat a path in LLB, thus only the name selects the type.
	if fromLLB(fsOpts.Source) {
		if fsOpts.Type == "" {
			fsOpts.Type = kraftfile.SourceTypeDirectory
			if isDockerfile(path.Base(fsOpts.Path)) {
				fsOpts.Type = kraftfile.SourceTypeDockerfile
			}
		}
		return resolveState(fsOpts)
	}

	if fsOpts.Type == "" {
		typ, err := DetectSourceType(fsOpts.Path)
		if err != nil {
			return err
		}
		fsOpts.Type = typ
	}

	return nil
}

func (b Builder) buildRoms(ctx context.Context, opts BuildOpts) (_ [][]imagespec.File, rerr error) {
	var romFiles [][]imagespec.File

	// Release anything already built if a later ROM fails.
	defer func() {
		if rerr == nil {
			return
		}
		for _, perPlat := range romFiles {
			for _, f := range perPlat {
				if f != nil {
					_ = f.Cleanup()
				}
			}
		}
	}()

	for i, rom := range opts.Roms {
		// The rom is built as a rootfs with all of its own options.
		rom.Format = cmp.Or(rom.Format, kraftfile.FsTypeErofs)
		romBuildOpts := BuildOpts{
			Rootfs: rom,
			// propagate BuildKit options from the parent build
			Platform: opts.Platform,
			BuildArg: opts.BuildArg,
			Target:   opts.Target,
			Secrets:  opts.Secrets,
			SSH:      opts.SSH,
			Contexts: opts.Contexts,
			NoCache:  opts.NoCache,
		}

		imgs, err := b.buildRootfs(ctx, romBuildOpts, fmt.Sprintf("rom %d", i+1))
		if err != nil {
			return nil, fmt.Errorf("building rom from %q: %w", rom.Path, err)
		}
		if len(imgs) != len(opts.Platform) {
			return nil, fmt.Errorf("rom build from %q produced %d images for %d platforms",
				rom.Path, len(imgs), len(opts.Platform))
		}

		perPlat := make([]imagespec.File, len(imgs))
		for i, img := range imgs {
			if img.Initrd == nil {
				return nil, fmt.Errorf("rom build from %q produced no output for platform %s",
					rom.Path, platforms.Format(opts.Platform[i]))
			}
			perPlat[i] = img.Initrd
			img.Initrd = nil // detach so Close doesn't close it
			img.Close()
		}

		romFiles = append(romFiles, perPlat)
	}
	return romFiles, nil
}

// BuildRootfs builds a rootfs for each platform in opts.Platform from the
// source at opts.Rootfs.Path.
func (b Builder) BuildRootfs(ctx context.Context, opts BuildOpts) ([]*imagespec.Image, error) {
	return b.buildRootfs(ctx, opts, "rootfs")
}

// buildRootfs builds part, the rootfs or a rom, for each platform in
// opts.Platform from the source at opts.Rootfs.Path, and reports every step
// of it under that name.
func (b Builder) buildRootfs(ctx context.Context, opts BuildOpts, part string) ([]*imagespec.Image, error) {
	if len(opts.Platform) == 0 {
		return nil, fmt.Errorf("at least one platform must be specified")
	}

	if err := localize(&opts.Rootfs); err != nil {
		return nil, err
	}
	fromState := fromLLB(opts.Rootfs.Source)
	if fromState {
		if err := resolveState(&opts.Rootfs); err != nil {
			return nil, err
		}
	}

	if opts.Rootfs.Format == "" &&
		opts.Rootfs.Type != kraftfile.SourceTypeCpio &&
		opts.Rootfs.Type != kraftfile.SourceTypeErofs &&
		opts.Rootfs.Type != kraftfile.SourceTypeOCI {
		opts.Rootfs.Format = defaultRootfsFormat(opts)
	}

	switch opts.Rootfs.Type {
	case kraftfile.SourceTypeCpio, kraftfile.SourceTypeErofs:
		expected := map[kraftfile.SourceType]kraftfile.FsType{
			kraftfile.SourceTypeCpio:  kraftfile.FsTypeCpio,
			kraftfile.SourceTypeErofs: kraftfile.FsTypeErofs,
		}
		if opts.Rootfs.Format != "" {
			if exp, ok := expected[opts.Rootfs.Type]; ok && opts.Rootfs.Format != exp {
				// TODO: maybe we could be smart and convert this
				return nil, fmt.Errorf("unsupported rootfs format mismatch: source is %s but requested format is %s", opts.Rootfs.Type, opts.Rootfs.Format)
			}
		} else {
			opts.Rootfs.Format = expected[opts.Rootfs.Type]
		}
		if opts.Rootfs.Type == kraftfile.SourceTypeCpio && !gocpio.IsValidPath(opts.Rootfs.Path) ||
			opts.Rootfs.Type == kraftfile.SourceTypeErofs && !goerofs.IsValidPath(opts.Rootfs.Path) {
			return nil, fmt.Errorf("malformed rootfs %s file %q", opts.Rootfs.Type, opts.Rootfs.Path)
		}

		return buildRootfsPackaged(ctx, opts)
	case kraftfile.SourceTypeDirectory:
		if fromState {
			return b.buildRootfsDirectoryState(ctx, opts, part)
		}
		return b.buildRootfsDirectory(ctx, opts, part)
	case kraftfile.SourceTypeTarball:
		return b.buildRootfsTarball(ctx, opts, part)
	case kraftfile.SourceTypeDockerfile:
		if fromState {
			return b.buildRootfsDockerfile(ctx, opts, part)
		}
		if _, err := os.Stat(opts.Rootfs.Path); err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("dockerfile context does not exist")
			}
			return nil, fmt.Errorf("checking dockerfile context path %q: %w", opts.Rootfs.Path, err)
		}
		if opts.Rootfs.Dockerfile != "" {
			dockerfilePath := filepath.Join(opts.Rootfs.Path, opts.Rootfs.Dockerfile)
			if _, err := os.Stat(dockerfilePath); err != nil {
				if os.IsNotExist(err) {
					return nil, fmt.Errorf("dockerfile %q does not exist in context %q", opts.Rootfs.Dockerfile, opts.Rootfs.Path)
				}
				return nil, fmt.Errorf("checking dockerfile path %q: %w", dockerfilePath, err)
			}
		}
		return b.buildRootfsDockerfile(ctx, opts, part)
	case kraftfile.SourceTypeOCI:
		return b.buildRootfsOCI(ctx, opts, part)
	default:
		return nil, fmt.Errorf("unsupported rootfs type %q", opts.Rootfs.Type)
	}
}

// buildRootfsFromPackaged returns images backed by an already-packaged rootfs
// file. The file is opened read-only per platform.
// The caller must not delete it.
func buildRootfsPackaged(_ context.Context, opts BuildOpts) (_ []*imagespec.Image, rerr error) {
	cfg := applyConfigOverrides(ocispec.ImageConfig{}, opts)

	var imgs []*imagespec.Image
	for _, p := range opts.Platform {
		f, err := os.Open(opts.Rootfs.Path)
		if err != nil {
			return nil, fmt.Errorf("opening pre-packaged rootfs: %w", err)
		}
		defer func() {
			if rerr != nil {
				f.Close()
			}
		}()

		imgs = append(imgs, imagespec.NewImage(
			imagespec.WithImageConfig(cfg),
			imagespec.WithPlatform(p),
			imagespec.WithInitrd(imagespec.NewOSFile(f)),
		))
	}
	return imgs, nil
}

// buildRootfsFromDirectory archives the source directory into a temporary
// rootfs file (CPIO or EroFS, based on opts.Rootfs.Format) for each platform.
func (b Builder) buildRootfsDirectory(ctx context.Context, opts BuildOpts, part string) ([]*imagespec.Image, error) {
	srcFS := os.DirFS(opts.Rootfs.Path)
	if runtime.GOOS == "windows" {
		srcFS = buildfs.WindowsDirFS(srcFS)
	}
	return b.packRootfs(ctx, opts, part, srcFS)
}

// buildRootfsDirectoryState packages an LLB source directory for each
// platform. The state can depend on the platform, thus each one has a solve.
func (b Builder) buildRootfsDirectoryState(ctx context.Context, opts BuildOpts, part string) (_ []*imagespec.Image, rerr error) {
	s, err := b.newSolver(ctx)
	if err != nil {
		return nil, err
	}
	defer s.close()

	if err := addBuildSessions(&s.session, opts); err != nil {
		return nil, err
	}

	var imgs []*imagespec.Image
	defer func() {
		if rerr != nil {
			for _, img := range imgs {
				_ = img.Close()
			}
		}
	}()

	for _, p := range opts.Platform {
		platOpts := opts
		platOpts.Platform = []ocispec.Platform{p}
		platImgs, err := b.packDirectoryState(ctx, s, platOpts, part)
		if err != nil {
			return nil, err
		}
		imgs = append(imgs, platImgs...)
	}
	return imgs, nil
}

// packDirectoryState solves a directory of a StateSource or a SessionSource on
// the one platform of opts, and packages it, reporting both as steps of part.
func (b Builder) packDirectoryState(ctx context.Context, s *solver, opts BuildOpts, part string) ([]*imagespec.Image, error) {
	dir := opts.Rootfs.Path
	st := sourceState(opts.Rootfs.Source, []string{dir})
	ep := getPlatform(opts.Platform[0])

	def, err := marshalState(ctx, st, ep.Platform, opts.NoCache)
	if err != nil {
		return nil, fmt.Errorf("marshalling source: %w", err)
	}
	dirDef, err := marshalState(ctx, subdir(st, dir), ep.Platform, opts.NoCache)
	if err != nil {
		return nil, fmt.Errorf("marshalling directory %q: %w", dir, err)
	}

	tarDest, err := os.CreateTemp(b.TempDir, "unikraft-buildkit-*.tar")
	if err != nil {
		return nil, fmt.Errorf("could not create temporary file: %w", err)
	}
	defer func() {
		tarDest.Close()
		os.Remove(tarDest.Name())
	}()

	err = s.solveToTar(ctx, tarDest, partPrefix(part, opts.Platform[0]), client.SolveOpt{},
		func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
			res, err := c.Solve(ctx, gateway.SolveRequest{
				Definition: def.ToPB(),
				Evaluate:   true,
			})
			if err != nil || dir == "." {
				return res, err
			}

			ref, err := res.SingleRef()
			if err != nil {
				return nil, err
			}
			if ref == nil {
				return nil, fmt.Errorf("the source is empty")
			}
			stat, err := ref.StatFile(ctx, gateway.StatRequest{Path: dir})
			if err != nil {
				return nil, err
			}
			if !os.FileMode(stat.Mode).IsDir() {
				return nil, fmt.Errorf("%q is not a directory", dir)
			}

			return c.Solve(ctx, gateway.SolveRequest{
				Definition: dirDef.ToPB(),
				Evaluate:   true,
			})
		})
	if err != nil {
		return nil, fmt.Errorf("solving directory %q of the source: %w", dir, err)
	}

	srcFS, err := buildfs.TarballFS(tarDest)
	if err != nil {
		return nil, fmt.Errorf("could not open tarball as filesystem: %w", err)
	}
	return b.packRootfs(ctx, opts, part, srcFS)
}

// buildRootfsTarball opens the source tarball as an fs.FS and packages it
// into the requested rootfs format (CPIO or EroFS) for each platform.
func (b Builder) buildRootfsTarball(ctx context.Context, opts BuildOpts, part string) ([]*imagespec.Image, error) {
	tarFile, err := os.Open(opts.Rootfs.Path)
	if err != nil {
		return nil, fmt.Errorf("could not open tarball: %w", err)
	}
	defer tarFile.Close()

	srcFS, err := buildfs.TarballFS(tarFile)
	if err != nil {
		return nil, fmt.Errorf("could not open tarball as filesystem: %w", err)
	}
	return b.packRootfs(ctx, opts, part, srcFS)
}

// packRootfs packages srcFS into a temporary rootfs file (CPIO or EroFS, based
// on opts.Rootfs.Format) for each platform, as a step of part.
func (b Builder) packRootfs(ctx context.Context, opts BuildOpts, part string, srcFS fs.FS) (_ []*imagespec.Image, rerr error) {
	cfg := applyConfigOverrides(ocispec.ImageConfig{}, opts)

	var imgs []*imagespec.Image
	for _, p := range opts.Platform {
		f, err := os.CreateTemp(b.TempDir, "unikraft-rootfs-*."+string(opts.Rootfs.Format))
		if err != nil {
			return nil, fmt.Errorf("could not create temporary file: %w", err)
		}
		defer func() {
			if rerr != nil && f != nil {
				f.Close()
				os.Remove(f.Name())
			}
		}()

		if err := b.pack(ctx, part, p, opts, opts.Rootfs.Format, f, srcFS); err != nil {
			return nil, err
		}

		imgs = append(imgs, imagespec.NewImage(
			imagespec.WithImageConfig(cfg),
			imagespec.WithPlatform(p),
			imagespec.WithInitrd(imagespec.NewTempOSFile(f)),
		))
	}
	return imgs, nil
}

// buildRootfsOCI pulls an OCI image and builds a rootfs from it for each
// requested platform. Two kinds of images are supported: Regular OCI and
// Unikraft images
func (b Builder) buildRootfsOCI(ctx context.Context, opts BuildOpts, part string) (_ []*imagespec.Image, rerr error) {
	access, err := b.images(ctx)
	if err != nil {
		return nil, err
	}

	uri, err := imagespec.ParseLocationDefault(opts.Rootfs.Path)
	if err != nil {
		return nil, fmt.Errorf("parsing rootfs image reference %q: %w", opts.Rootfs.Path, err)
	}

	imagePlatforms := getPlatforms(opts.Platform)
	wanted := make([]ocispec.Platform, 0, 2*len(opts.Platform))
	for i, p := range opts.Platform {
		wanted = append(wanted, p, imagePlatforms[i].Platform)
	}

	matcher := platforms.Any(wanted...)
	var loaded []*imagespec.Image
	verb := "load"
	if uri.Scheme == schemes.OCI {
		verb = "pull"
	}
	err = progress.Step(progress.WithPrefix(b.progressWriter(), part), verb+" "+opts.Rootfs.Path, func() (err error) {
		res, err := access.Resolve(ctx, uri)
		if err != nil {
			return fmt.Errorf("resolving rootfs image %q: %w", opts.Rootfs.Path, err)
		}
		loaded, err = access.LoadAll(ctx, res, matcher)
		if err != nil {
			return fmt.Errorf("pulling rootfs image %q: %w", opts.Rootfs.Path, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, img := range loaded {
			_ = img.Close()
		}
	}()

	byPlatform := make(map[string]*imagespec.Image, len(loaded))
	for _, img := range loaded {
		if img.Image == nil {
			continue
		}
		byPlatform[platforms.Format(platforms.Normalize(img.Image.Platform))] = img
	}

	flattened := make(map[*imagespec.Image]fs.FS, len(loaded))
	var s *solver

	var imgs []*imagespec.Image
	for i, p := range opts.Platform {
		src := byPlatform[platforms.Format(platforms.Normalize(p))]
		if src == nil {
			src = byPlatform[platforms.Format(imagePlatforms[i].Platform)]
		}
		if src == nil {
			if len(loaded) == 1 && len(opts.Platform) == 1 {
				src = loaded[0]
			} else {
				return nil, fmt.Errorf("rootfs image %q does not contain platform %q", opts.Rootfs.Path, platforms.Format(p))
			}
		}

		cfg := applyConfigOverrides(ocispec.ImageConfig{}, opts)
		if src.Image != nil {
			cfg = applyConfigOverrides(src.Image.Config, opts)
		}

		// Date the rootfs by its source, so that a conversion is reproducible.
		created := imagespec.ImageMetadata{Created: src.Metadata().Created}

		if src.Initrd != nil {
			f, err := os.CreateTemp(b.TempDir, "unikraft-rootfs-*")
			if err != nil {
				return nil, fmt.Errorf("could not create temporary file: %w", err)
			}
			defer func() {
				if rerr != nil && f != nil {
					f.Close()
					os.Remove(f.Name())
				}
			}()

			w := progress.WithPrefix(b.progressWriter(), partPrefix(part, p))
			if err := progress.Step(w, "copy initrd of "+opts.Rootfs.Path, func() error {
				return copyInitrd(ctx, f, src.Initrd, opts, b.MaxRootfsSize)
			}); err != nil {
				return nil, err
			}

			imgs = append(imgs, imagespec.NewImage(
				imagespec.WithImageConfig(cfg),
				imagespec.WithPlatform(p),
				imagespec.WithInitrd(imagespec.NewTempOSFile(f)),
				imagespec.WithImageMetadata(created),
			))
			continue
		}

		format := cmp.Or(opts.Rootfs.Format, defaultRootfsFormat(opts))

		srcFS, ok := flattened[src]
		if !ok {
			if uri.Scheme != schemes.OCI {
				return nil, fmt.Errorf("rootfs image %q must be a registry reference, %q is not supported", uri.Path, uri.Scheme)
			}
			if src.Image == nil {
				return nil, fmt.Errorf("rootfs image %q has no config to take a platform from", uri.Path)
			}
			if s == nil {
				s, err = b.newSolver(ctx)
				if err != nil {
					return nil, err
				}
				defer s.close()
			}
			layers, err := os.CreateTemp(b.TempDir, "unikraft-buildkit-*.tar")
			if err != nil {
				return nil, fmt.Errorf("could not create temporary file: %w", err)
			}
			defer func() {
				layers.Close()
				os.Remove(layers.Name())
			}()

			if err := flattenImageLayers(ctx, s, opts, src, uri, layers, partPrefix(part, p)); err != nil {
				return nil, err
			}

			srcFS, err = buildfs.TarballFS(layers)
			if err != nil {
				return nil, fmt.Errorf("could not open flattened rootfs image as filesystem: %w", err)
			}
			flattened[src] = srcFS
		}

		f, err := os.CreateTemp(b.TempDir, "unikraft-rootfs-*."+string(format))
		if err != nil {
			return nil, fmt.Errorf("could not create temporary file: %w", err)
		}
		defer func() {
			if rerr != nil && f != nil {
				f.Close()
				os.Remove(f.Name())
			}
		}()

		if err := b.pack(ctx, part, p, opts, format, f, srcFS); err != nil {
			return nil, err
		}

		imgs = append(imgs, imagespec.NewImage(
			imagespec.WithImageConfig(cfg),
			imagespec.WithPlatform(p),
			imagespec.WithInitrd(imagespec.NewTempOSFile(f)),
			imagespec.WithImageMetadata(created),
		))
	}

	return imgs, nil
}

// copyInitrd writes the initrd of a Unikraft image to f as it is, padded as
// opts asks, and checks that its format is the one opts asks for. An initrd
// of more than limit bytes fails with ErrTooLarge.
func copyInitrd(ctx context.Context, f *os.File, initrd imagespec.File, opts BuildOpts, limit int64) error {
	rc, _, err := initrd.Open(ctx)
	if err != nil {
		return fmt.Errorf("opening rootfs layer: %w", err)
	}
	if _, err := io.Copy(&limitWriter{w: f, max: limit}, rc); err != nil {
		rc.Close()
		return fmt.Errorf("reading rootfs layer: %w", err)
	}
	if err := rc.Close(); err != nil {
		return fmt.Errorf("closing rootfs layer: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("could not sync file: %w", err)
	}

	format, err := detectPackagedFormat(f.Name())
	if err != nil {
		return fmt.Errorf("inspecting initrd of rootfs image %q: %w", opts.Rootfs.Path, err)
	}
	if opts.Rootfs.Format != "" && opts.Rootfs.Format != format {
		return fmt.Errorf("unsupported rootfs format mismatch: source is %s but requested format is %s", format, opts.Rootfs.Format)
	}

	if err := padFile(f, opts.Rootfs.Pad); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("could not sync file: %w", err)
	}

	return nil
}

// flattenImageLayers writes the flattened filesystem of a regular OCI image to
// dst as an uncompressed tarball, using BuildKit to do the flattening. The
// image must come from a registry and have a config. The progress of the
// solve is reported under prefix.
func flattenImageLayers(ctx context.Context, s *solver, opts BuildOpts, src *imagespec.Image, uri *imagespec.Location, dst *os.File, prefix string) error {
	ref := uri.Path
	if src.ManifestDescriptor.Digest != "" && !strings.Contains(ref, "@") {
		ref += "@" + src.ManifestDescriptor.Digest.String()
	}

	ep := getPlatform(src.Image.Platform)

	imageOpts := []llb.ImageOption{llb.Platform(ep.Platform)}
	constraints := []llb.ConstraintsOpt{llb.Platform(ep.Platform)}
	if opts.NoCache {
		imageOpts = append(imageOpts, llb.ResolveModeForcePull)
		constraints = append(constraints, llb.IgnoreCache)
	}

	def, err := llb.Image(ref, imageOpts...).Marshal(ctx, constraints...)
	if err != nil {
		return fmt.Errorf("marshalling rootfs image source: %w", err)
	}

	err = s.solveToTar(ctx, dst, prefix, client.SolveOpt{},
		func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
			return c.Solve(ctx, gateway.SolveRequest{
				Definition: def.ToPB(),
				Evaluate:   true,
			})
		})
	if err != nil {
		return fmt.Errorf("flattening rootfs image %q: %w", uri.Path, err)
	}

	return nil
}

func (b Builder) buildRootfsDockerfile(ctx context.Context, opts BuildOpts, part string) (_ []*imagespec.Image, rerr error) {
	s, err := b.newSolver(ctx)
	if err != nil {
		return nil, err
	}
	defer s.close()

	attrs := map[string]string{}
	localDirs := map[string]string{}
	inputs := map[string]llb.State{}
	if err := applyBuildOpts(attrs, localDirs, inputs, &s.session, opts); err != nil {
		return nil, err
	}
	localMounts := make(map[string]fsutil.FS, len(localDirs))
	for name, dir := range localDirs {
		mount, err := fsutil.NewFS(dir)
		if err != nil {
			return nil, fmt.Errorf("could not open local directory %q: %w", dir, err)
		}
		localMounts[name] = mount
	}

	expPlatforms := getPlatforms(opts.Platform)

	// NOTE: solving all platforms in one export corrupts symlinks (a
	// buildkit bug), so solve and export each platform separately:
	// https://github.com/moby/buildkit/issues/6684
	var imgs []*imagespec.Image
	for i, p := range opts.Platform {
		ep := expPlatforms[i]

		platformAttrs := maps.Clone(attrs)
		platformAttrs["platform"] = ep.ID

		frontendInputs := make(map[string]*pb.Definition, len(inputs))
		for name, st := range inputs {
			def, err := marshalState(ctx, st, ep.Platform, opts.NoCache)
			if err != nil {
				return nil, fmt.Errorf("marshalling %s input: %w", name, err)
			}
			frontendInputs[name] = def.ToPB()
		}

		tarDest, err := os.CreateTemp(b.TempDir, "unikraft-buildkit-*.tar")
		if err != nil {
			return nil, fmt.Errorf("could not create temporary file: %w", err)
		}
		defer func() {
			tarDest.Close()
			os.Remove(tarDest.Name())
		}()

		solveOpt := client.SolveOpt{
			LocalMounts:   localMounts,
			Frontend:      "dockerfile.v0",
			FrontendAttrs: platformAttrs,
		}

		var config ocispec.Image
		err = s.solveToTar(ctx, tarDest, partPrefix(part, p), solveOpt, func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
			res, err := c.Solve(ctx, gateway.SolveRequest{
				Frontend:       solveOpt.Frontend,
				FrontendOpt:    solveOpt.FrontendAttrs,
				FrontendInputs: frontendInputs,
			})
			if err != nil {
				return nil, err
			}
			cfg := exptypes.ParseKey(res.Metadata, "containerimage.config", &ep)
			if cfg == nil {
				return nil, fmt.Errorf("could not find config for platform %s in build result metadata", ep.ID)
			}
			if err := json.Unmarshal(cfg, &config); err != nil {
				return nil, err
			}
			return res, nil
		})
		if err != nil {
			return nil, err
		}

		srcFS, err := buildfs.TarballFS(tarDest)
		if err != nil {
			return nil, fmt.Errorf("could not open tarball as filesystem: %w", err)
		}

		f, err := os.CreateTemp(b.TempDir, "unikraft-rootfs-*."+string(opts.Rootfs.Format))
		if err != nil {
			return nil, fmt.Errorf("could not create temporary file: %w", err)
		}
		defer func() {
			if rerr != nil && f != nil {
				f.Close()
				os.Remove(f.Name())
			}
		}()

		if err := b.pack(ctx, part, p, opts, opts.Rootfs.Format, f, srcFS); err != nil {
			return nil, err
		}

		imgs = append(imgs, imagespec.NewImage(
			imagespec.WithImageConfig(applyConfigOverrides(config.Config, opts)),
			imagespec.WithPlatform(p),
			imagespec.WithInitrd(imagespec.NewTempOSFile(f)),
		))
	}

	return imgs, nil
}

// partPrefix names the part of the image a step builds and the platform it
// builds it for, the way buildkit names the steps of a multi-platform build.
func partPrefix(part string, p ocispec.Platform) string {
	return part + " " + platforms.Format(p)
}

// pack writes srcFS to destFS in format, as the step that packs part for
// platform p.
func (b Builder) pack(ctx context.Context, part string, p ocispec.Platform, opts BuildOpts, format kraftfile.FsType, destFS *os.File, srcFS fs.FS) error {
	w := progress.WithPrefix(b.progressWriter(), partPrefix("pack", p))
	return progress.Step(w, part+" as "+string(format), func() error {
		if err := checkDataSize(srcFS, b.MaxRootfsSize); err != nil {
			return err
		}
		return packageFS(ctx, format, destFS, srcFS, opts.Rootfs)
	})
}

// Repack returns the rootfs f of platform p in format, packed again when it is
// in another format. Only an erofs rootfs can be packed again, as cpio.
func (b Builder) Repack(ctx context.Context, f imagespec.File, p ocispec.Platform, format kraftfile.FsType) (_ imagespec.File, rerr error) {
	rc, _, err := f.Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("opening rootfs: %w", err)
	}
	defer rc.Close()
	src, ok := rc.(io.ReaderAt)
	if !ok {
		// The format check and erofs need random access, thus copy a stream to a file.
		tmp, err := os.CreateTemp(b.TempDir, "unikraft-rootfs-*")
		if err != nil {
			return nil, fmt.Errorf("could not create temporary file: %w", err)
		}
		defer func() {
			tmp.Close()
			os.Remove(tmp.Name())
		}()
		if _, err := io.Copy(tmp, rc); err != nil {
			return nil, fmt.Errorf("reading rootfs: %w", err)
		}
		src = tmp
	}

	var from kraftfile.FsType
	switch {
	case gocpio.IsValid(io.NewSectionReader(src, 0, 6)):
		from = kraftfile.FsTypeCpio
	case goerofs.IsValid(io.NewSectionReader(src, goerofs.SuperBlockOffset, 4)):
		from = kraftfile.FsTypeErofs
	default:
		return nil, errors.New("could not detect rootfs format")
	}
	switch {
	case from == format:
		return f.Clone()
	case from != kraftfile.FsTypeErofs || format != kraftfile.FsTypeCpio:
		return nil, fmt.Errorf("unsupported rootfs conversion from %s to %s", from, format)
	}

	srcFS, err := goerofs.Open(src)
	if err != nil {
		return nil, fmt.Errorf("reading rootfs: %w", err)
	}
	dest, err := os.CreateTemp(b.TempDir, "unikraft-rootfs-*."+string(format))
	if err != nil {
		return nil, fmt.Errorf("could not create temporary file: %w", err)
	}
	defer func() {
		if rerr != nil {
			dest.Close()
			os.Remove(dest.Name())
		}
	}()

	if err := b.pack(ctx, "rootfs", p, BuildOpts{}, format, dest, srcFS); err != nil {
		return nil, err
	}

	return imagespec.NewTempOSFile(dest), nil
}

func packageFS(ctx context.Context, format kraftfile.FsType, destFS *os.File, srcFS fs.FS, opts FSOpts) error {
	log.G(ctx).
		Debug().
		Str("format", string(format)).
		Msg("packaging rootfs")

	// Owners of a host directory apply only to the host, so set them to root.
	// Keep the owners from all other sources, as Docker does.
	_, fromState := opts.Source.(StateSource)
	allRoot := opts.Type == kraftfile.SourceTypeDirectory && !fromState

	switch format {
	case kraftfile.FsTypeCpio:
		var gw *gzip.Writer
		var w io.Writer = destFS
		if opts.Compress {
			gw = gzip.NewWriter(w)
			w = gw
		}

		if err := buildfs.CreateCPIO(ctx, w, srcFS,
			buildfs.WithAllRoot(allRoot),
		); err != nil {
			return fmt.Errorf("could not create CPIO archive: %w", err)
		}

		if gw != nil {
			if err := gw.Close(); err != nil {
				return fmt.Errorf("could not close gzip writer: %w", err)
			}
		}
	case kraftfile.FsTypeErofs:
		if opts.Compress {
			log.G(ctx).Warn().Msg("compression is not supported for EROFS, ignoring compress option")
		}

		if err := buildfs.CreateEROFS(destFS, srcFS,
			buildfs.WithAllRoot(allRoot),
		); err != nil {
			return fmt.Errorf("could not create EroFS archive: %w", err)
		}
	default:
		return fmt.Errorf("unknown filesystem type %q", format)
	}

	if err := padFile(destFS, opts.Pad); err != nil {
		return err
	}

	if err := destFS.Sync(); err != nil {
		return fmt.Errorf("could not sync file: %w", err)
	}

	return nil
}

// padFile pads f up to a multiple of pad bytes.
func padFile(f *os.File, pad int64) error {
	if pad <= 0 {
		return nil
	}

	pos, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("could not seek to end of file: %w", err)
	}
	if rem := pos % pad; rem != 0 {
		padding := make([]byte, pad-rem)
		if _, err := f.Write(padding); err != nil {
			return fmt.Errorf("could not pad file to page alignment: %w", err)
		}
	}

	return nil
}

func applyBuildOpts(attrs map[string]string, localDirs map[string]string, inputs map[string]llb.State, sessions *[]session.Attachable, opts BuildOpts) error {
	// The frontend reads the Dockerfile from the full state or from its own
	// source, and the context from its directory in the state.
	if fromLLB(opts.Rootfs.Source) {
		dir, filename, dockerfile := path.Dir(opts.Rootfs.Path), opts.Rootfs.Path, opts.Rootfs.Source
		if opts.Rootfs.DockerfileSource != nil {
			dir, filename, dockerfile = opts.Rootfs.Path, opts.Rootfs.Dockerfile, opts.Rootfs.DockerfileSource
		} else if opts.Rootfs.Dockerfile != "" {
			dir, filename = opts.Rootfs.Path, path.Join(opts.Rootfs.Path, opts.Rootfs.Dockerfile)
		}
		attrs["filename"] = filename
		// Send all of the Dockerfile on each sync, as the frontend does, because
		// the metadata of a changed file can stay the same.
		inputs["dockerfile"] = sourceState(dockerfile, []string{filename, filename + ".dockerignore"}, llb.Differ(llb.DiffNone, false))
		// Without an input, the frontend reads the local "context" and
		// applies its .dockerignore.
		if src, ok := opts.Rootfs.Source.(SessionSource); !ok || src.Name != "context" || dir != "." {
			inputs["context"] = subdir(sourceState(opts.Rootfs.Source, []string{dir}), dir)
		}
	} else if opts.Rootfs.Dockerfile != "" {
		localDirs["context"] = opts.Rootfs.Path
		localDirs["dockerfile"] = filepath.Join(opts.Rootfs.Path, filepath.Dir(opts.Rootfs.Dockerfile))
		attrs["filename"] = filepath.Base(opts.Rootfs.Dockerfile)
	} else {
		localDirs["context"] = filepath.Dir(opts.Rootfs.Path)
		localDirs["dockerfile"] = filepath.Dir(opts.Rootfs.Path)
		attrs["filename"] = filepath.Base(opts.Rootfs.Path)
	}
	if opts.Target != "" {
		attrs["target"] = opts.Target
	}

	if opts.NoCache {
		attrs["no-cache"] = ""
	}

	for name, src := range opts.Contexts {
		name, err := contextName(name)
		if err != nil {
			return err
		}
		if _, ok := attrs["context:"+name]; ok {
			return fmt.Errorf("two contexts have the name %q", name)
		}
		switch src := src.(type) {
		case StateSource:
			inputs[name] = src.State
			attrs["context:"+name] = "input:" + name
		case LocalSource:
			localDirs[name] = src.Dir
			attrs["context:"+name] = "local:" + name
		case SessionSource:
			if src.Name == "" {
				return fmt.Errorf("the session source of context %q needs a name", name)
			}
			attrs["context:"+name] = "local:" + src.Name
		case FrontendSource:
			if src.Ref == "" {
				return fmt.Errorf("the frontend reference of context %q is empty", name)
			}
			attrs["context:"+name] = src.Ref
			attrs["frontend.caps"] = "moby.buildkit.frontend.contexts+forward"
		default:
			return fmt.Errorf("unsupported source %T of context %q", src, name)
		}
	}

	// The local directories add a second filesync to the session, and gRPC
	// exits the process when a service registers two times.
	if len(localDirs) > 0 && hasSession(opts) {
		return fmt.Errorf("a Dockerfile and its contexts cannot mix local and session sources")
	}

	for _, buildArg := range opts.BuildArg {
		if buildArg == "" {
			continue
		}
		key, val, ok := strings.Cut(buildArg, "=")
		if key == "" {
			return fmt.Errorf("invalid build-arg %q", buildArg)
		}
		if !ok {
			val, _ = os.LookupEnv(key)
		}
		attrs["build-arg:"+key] = val
	}

	return addBuildSessions(sessions, opts)
}

// contextName returns name as the frontend finds a context: the familiar
// name of an image reference without the latest tag, as buildx gives it.
func contextName(name string) (string, error) {
	named, err := distref.ParseNormalizedNamed(name)
	if err != nil {
		return "", fmt.Errorf("invalid context name %q: %w", name, err)
	}
	familiar := strings.TrimSuffix(distref.FamiliarString(named), ":latest")
	// The context and dockerfile names are the frontend's own inputs.
	if familiar == "context" || familiar == "dockerfile" {
		return "", fmt.Errorf("invalid context name %q", name)
	}
	return familiar, nil
}

// addBuildSessions adds the secret and SSH providers of opts to sessions.
func addBuildSessions(sessions *[]session.Attachable, opts BuildOpts) error {
	if len(opts.Secrets) > 0 {
		provider, err := buildflags.CreateSecrets(opts.Secrets)
		if err != nil {
			return err
		}
		*sessions = append(*sessions, provider)
	}
	if len(opts.SSH) > 0 {
		provider, err := buildflags.CreateSSH(opts.SSH)
		if err != nil {
			return err
		}
		*sessions = append(*sessions, provider)
	}

	return nil
}

// getPlatform maps a unikraft target platform onto the linux platform BuildKit
// builds for.
func getPlatform(p ocispec.Platform) exptypes.Platform {
	p.OS = "linux"
	p.OSFeatures = nil
	p.OSVersion = ""
	p = platforms.Normalize(p)
	return exptypes.Platform{
		ID:       platforms.Format(p),
		Platform: p,
	}
}

func getPlatforms(ps []ocispec.Platform) (exp []exptypes.Platform) {
	for _, p := range ps {
		exp = append(exp, getPlatform(p))
	}
	return exp
}
