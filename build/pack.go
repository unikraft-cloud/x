// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	gatewaypb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/identity"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"google.golang.org/grpc"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/image-spec/reference"
	"unikraft.com/x/image-spec/schemes"
	"unikraft.com/x/kraftfile"

	"unikraft.com/x/build/internal/buildfs"
	"unikraft.com/x/build/progress"
)

// DefaultPacker is the packer image for Builder.Packer that publish-packer
// builds from cmd/packer/Dockerfile.
const DefaultPacker = "index.unikraft.io/official/utils/image-packer:latest"

// pack writes srcFS to destFS in format, as the step that packs part for
// platform p.
func (b Builder) pack(ctx context.Context, part string, p ocispec.Platform, opts BuildOpts, format kraftfile.FsType, destFS *os.File, srcFS fs.FS) error {
	w := progress.WithPrefix(b.progressWriter(), partPrefix("pack", p))
	return progress.Step(w, part+" as "+string(format), func() error {
		if err := buildfs.CheckSize(srcFS, b.MaxRootfsSize); err != nil {
			return err
		}
		// Owners of a host directory apply only to the host, so set them to root.
		// Keep the owners from all other sources, as Docker does.
		_, fromState := opts.Rootfs.Source.(StateSource)
		allRoot := opts.Rootfs.Type == kraftfile.SourceTypeDirectory && !fromState
		return buildfs.Package(ctx, format, destFS, srcFS,
			buildfs.WithAllRoot(allRoot),
			buildfs.WithPad(opts.Rootfs.Pad),
			buildfs.WithCompress(opts.Rootfs.Compress),
		)
	})
}

// authSSHID is the SSH forward that takes the requests of the packer for
// registry credentials to Builder.Credentials.
const authSSHID = "unikraft-registry-auth"

// packerPush is how the packer pushes the image of each platform from its
// solve: with the runtime's kernel, to a temporary tag in each repository.
type packerPush struct {
	runtime string                // pinned reference of the runtime
	repos   []string              // of BuildOpts.Push
	tag     string                // prefix of the temporary tags
	socket  string                // serving Builder.Credentials, empty without
	now     time.Time             // of the build, for the images' dates
	kernels map[string]*time.Time // dates of the kernels by platform
}

// startPush has the packer of a build with kernels from a runtime push to the
// repositories of opts.Push, and serves it Builder.Credentials until stop.
func (b Builder) startPush(opts *BuildOpts, kernels []*imagespec.Image, now time.Time) (stop func(), err error) {
	push := &packerPush{
		runtime: kernels[0].Location.Path,
		tag:     "tmp-" + identity.NewID()[:12],
		now:     now,
		kernels: make(map[string]*time.Time, len(kernels)),
	}
	for _, kernel := range kernels {
		push.kernels[platforms.Format(kernel.Image.Platform)] = kernel.Metadata().Created
	}
	for _, dest := range opts.Push {
		if dest.Scheme != schemes.OCI {
			return nil, fmt.Errorf("the packer pushes only to a registry, not to %s", dest)
		}
		named, err := reference.ParseNormalizedNamed(dest.Path)
		if err != nil {
			return nil, fmt.Errorf("parsing image reference %q: %w", dest.Path, err)
		}
		push.repos = append(push.repos, named.Name())
	}

	stop = func() {}
	if b.Credentials != nil {
		// Not in TempDir, which can be too long for the path of a socket.
		dir, err := os.MkdirTemp("", "unikraft-auth-*")
		if err != nil {
			return nil, fmt.Errorf("could not create temporary directory: %w", err)
		}
		push.socket = filepath.Join(dir, "auth.sock")
		l, err := net.Listen("unix", push.socket)
		if err != nil {
			os.RemoveAll(dir)
			return nil, fmt.Errorf("serving registry credentials: %w", err)
		}
		srv := grpc.NewServer()
		b.Credentials.Attachable().Register(srv)
		go func() { _ = srv.Serve(l) }()
		stop = func() {
			srv.Stop()
			os.RemoveAll(dir)
		}
	}
	opts.push = push
	return stop, nil
}

// pushedTag returns the temporary tag that the packer pushed img under, or ""
// for an image that the packer did not push.
func pushedTag(img *imagespec.Image) string {
	tag := img.Reference.Tag()
	if img.Kernel != nil || img.KernelDebug != nil || img.Initrd != nil || len(img.Roms) > 0 ||
		img.ManifestDescriptor.Digest == "" || !strings.HasPrefix(tag, "tmp-") {
		return ""
	}
	return tag
}

// tagOf returns the temporary tag of the image of platform p.
func (push *packerPush) tagOf(p ocispec.Platform) string {
	return push.tag + "-" + strings.ReplaceAll(platforms.Format(p), "/", "-")
}

// image returns the config of the image of platform p with cfg, dated by its
// newest part, of which rootfs is the date of the rootfs.
func (push *packerPush) image(p ocispec.Platform, cfg ocispec.ImageConfig, rootfs *time.Time) ocispec.Image {
	created := newest(push.now, push.kernels[platforms.Format(p)], rootfs)
	return ocispec.Image{Created: &created, Platform: p, Config: cfg}
}

// pushedImage returns the image of img that the packer pushed, from the
// descriptor of its manifest in f, which it removes.
func (push *packerPush) pushedImage(f *os.File, img ocispec.Image) (*imagespec.Image, error) {
	f.Close()
	data, err := os.ReadFile(f.Name())
	os.Remove(f.Name())
	if err != nil {
		return nil, err
	}
	var desc ocispec.Descriptor
	if err := json.Unmarshal(data, &desc); err != nil {
		return nil, fmt.Errorf("reading the descriptor of the pushed image: %w", err)
	}
	ref, err := reference.Parse(push.repos[0] + ":" + push.tagOf(img.Platform))
	if err != nil {
		return nil, err
	}

	pushed := imagespec.NewImage(
		imagespec.WithImageConfig(img.Config),
		imagespec.WithPlatform(img.Platform),
		imagespec.WithImageMetadata(imagespec.ImageMetadata{Created: img.Created}),
	)
	pushed.ManifestDescriptor = desc
	pushed.Reference = ref
	return pushed, nil
}

// packArgs is the packer command line that packs /src as format into
// /out/initrd, the way opts ask, refusing a rootfs over maxSize bytes.
func packArgs(opts BuildOpts, format kraftfile.FsType, maxSize int64) []string {
	args := []string{
		"/pack", "pack",
		"--format=" + string(format),
		"--pad=" + strconv.FormatInt(opts.Rootfs.Pad, 10),
		"--max-size=" + strconv.FormatInt(maxSize, 10),
	}
	if opts.Rootfs.Compress {
		args = append(args, "--compress")
	}
	// Owners of a session directory apply only to the caller's host, so set
	// them to root. Keep the owners from all other sources, as Docker does.
	if _, session := opts.Rootfs.Source.(SessionSource); session && opts.Rootfs.Type == kraftfile.SourceTypeDirectory {
		args = append(args, "--all-root")
	}
	return append(args, "/src", "/out/initrd")
}

// packState returns rootfs packed as format, in a state holding /initrd. The
// packer runs on the daemon's own platform, as the format does not depend on
// the target's, and refuses a rootfs over Builder.MaxRootfsSize.
func (b Builder) packState(c gateway.Client, rootfs llb.State, format kraftfile.FsType, opts BuildOpts) llb.State {
	native := nativePlatform(c)
	runOpts := []llb.RunOption{
		llb.Args(packArgs(opts, format, b.MaxRootfsSize)),
		llb.AddMount("/src", rootfs, llb.Readonly),
		llb.Network(llb.NetModeNone),
		llb.Platform(native),
		llb.WithCustomName("pack as " + string(format)),
	}
	if opts.NoCache {
		runOpts = append(runOpts, llb.IgnoreCache)
	}
	packer := llb.Image(b.Packer, llb.Platform(native))
	return packer.Run(runOpts...).AddMount("/out", llb.Scratch())
}

// pushState returns a state holding /manifest.json, the descriptor of the
// image img that the packer pushes with the rootfs that packed holds.
func (b Builder) pushState(c gateway.Client, packed llb.State, img ocispec.Image, opts BuildOpts) (llb.State, error) {
	data, err := json.Marshal(img)
	if err != nil {
		return llb.State{}, err
	}
	native := nativePlatform(c)
	args := []string{"/pack", "push", "--runtime=" + opts.push.runtime, "--config=/config/config.json"}
	runOpts := []llb.RunOption{
		llb.AddMount("/initrd", packed, llb.Readonly),
		llb.AddMount("/config", llb.Scratch().File(llb.Mkfile("/config.json", 0o644, data)), llb.Readonly),
		// image-spec stages the initrd layer in /tmp.
		llb.AddMount("/tmp", llb.Scratch()),
		llb.Platform(native),
	}
	if opts.push.socket != "" {
		args = append(args, "--auth-socket=/run/auth.sock")
		runOpts = append(runOpts, llb.AddSSHSocket(llb.SSHID(authSSHID), llb.SSHSocketTarget("/run/auth.sock")))
	}
	args = append(args, "/initrd/initrd", "/out/manifest.json")
	targets := make([]string, len(opts.push.repos))
	for i, repo := range opts.push.repos {
		targets[i] = repo + ":" + opts.push.tagOf(img.Platform)
	}
	runOpts = append(runOpts,
		llb.Args(append(args, targets...)),
		llb.WithCustomName("push to "+strings.Join(targets, ", ")),
	)
	packer := llb.Image(b.Packer, llb.Platform(native))
	return packer.Run(runOpts...).AddMount("/out", llb.Scratch()), nil
}

// nativePlatform returns the platform of the daemon's first worker.
func nativePlatform(c gateway.Client) ocispec.Platform {
	if ws := c.BuildOpts().Workers; len(ws) > 0 && len(ws[0].Platforms) > 0 {
		return ws[0].Platforms[0]
	}
	return platforms.DefaultSpec()
}

// solveInitrd packs the rootfs that build evaluates as format, in the solve with
// Builder.Packer and on the caller without, and returns it in a temporary file
// named after format. A rootfs over the size limit fails with ErrTooLarge.
// With opts.push, the packer also pushes the image that config gives after
// build, and the file holds the descriptor of its manifest instead.
func (b Builder) solveInitrd(ctx context.Context, s *solver, opts BuildOpts, part string, p ocispec.Platform, format kraftfile.FsType, solveOpt client.SolveOpt, build gateway.BuildFunc, config func() ocispec.Image) (_ *os.File, rerr error) {
	tarDest, err := os.CreateTemp(b.TempDir, "unikraft-buildkit-*.tar")
	if err != nil {
		return nil, fmt.Errorf("could not create temporary file: %w", err)
	}
	defer func() {
		tarDest.Close()
		os.Remove(tarDest.Name())
	}()

	f, err := os.CreateTemp(b.TempDir, "unikraft-rootfs-*."+string(format))
	if err != nil {
		return nil, fmt.Errorf("could not create temporary file: %w", err)
	}
	defer func() {
		if rerr != nil {
			f.Close()
			os.Remove(f.Name())
		}
	}()

	if b.Packer == "" {
		if err := s.solveToTar(ctx, tarDest, partPrefix(part, p), solveOpt, build); err != nil {
			return nil, err
		}
		srcFS, err := buildfs.TarballFS(tarDest)
		if err != nil {
			return nil, fmt.Errorf("could not open tarball as filesystem: %w", err)
		}
		if err := b.pack(ctx, part, p, opts, format, f, srcFS); err != nil {
			return nil, err
		}
		return f, nil
	}

	var refused bool
	err = s.solveToTar(ctx, tarDest, partPrefix(part, p), solveOpt, func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res, err := build(ctx, c)
		if err != nil {
			return nil, err
		}
		ref, err := res.SingleRef()
		if err != nil {
			return nil, err
		}
		rootfs := llb.Scratch()
		if ref != nil { // an empty rootfs has no reference
			if rootfs, err = ref.ToState(); err != nil {
				return nil, err
			}
		}
		st := b.packState(c, rootfs, format, opts)
		if opts.push != nil {
			if st, err = b.pushState(c, st, config(), opts); err != nil {
				return nil, err
			}
		}
		def, err := st.Marshal(ctx)
		if err != nil {
			return nil, fmt.Errorf("marshalling the pack step: %w", err)
		}
		// build evaluated the rootfs, thus only the packer runs in this solve.
		res, err = c.Solve(ctx, gateway.SolveRequest{Definition: def.ToPB(), Evaluate: true})
		if exit, ok := errors.AsType[*gatewaypb.ExitError](err); ok && exit.ExitCode == buildfs.ExitCodeTooLarge {
			refused = true
		}
		return res, err
	})
	if refused {
		return nil, fmt.Errorf("%w: more than %d bytes", ErrTooLarge, b.MaxRootfsSize)
	}
	if err != nil {
		return nil, err
	}

	name := "initrd"
	if opts.push != nil {
		name = "manifest.json"
	}
	tr := tar.NewReader(tarDest)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("the solve gave no %s", name)
		}
		if err != nil {
			return nil, fmt.Errorf("reading the packed rootfs: %w", err)
		}
		if strings.TrimPrefix(hdr.Name, "./") == name && hdr.Typeflag == tar.TypeReg {
			break
		}
	}
	if _, err := io.Copy(f, tr); err != nil {
		return nil, fmt.Errorf("writing the packed rootfs: %w", err)
	}
	return f, nil
}
