// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	gatewaypb "github.com/moby/buildkit/frontend/gateway/pb"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
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
	native := platforms.DefaultSpec()
	if ws := c.BuildOpts().Workers; len(ws) > 0 && len(ws[0].Platforms) > 0 {
		native = ws[0].Platforms[0]
	}
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

// solveInitrd packs the rootfs that build evaluates as format, in the solve with
// Builder.Packer and on the caller without, and returns it in a temporary file
// named after format. A rootfs over the size limit fails with ErrTooLarge.
func (b Builder) solveInitrd(ctx context.Context, s *solver, opts BuildOpts, part string, p ocispec.Platform, format kraftfile.FsType, solveOpt client.SolveOpt, build gateway.BuildFunc) (_ *os.File, rerr error) {
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
		def, err := b.packState(c, rootfs, format, opts).Marshal(ctx)
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

	tr := tar.NewReader(tarDest)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("the solve gave no initrd")
		}
		if err != nil {
			return nil, fmt.Errorf("reading the packed rootfs: %w", err)
		}
		if strings.TrimPrefix(hdr.Name, "./") == "initrd" && hdr.Typeflag == tar.TypeReg {
			break
		}
	}
	if _, err := io.Copy(f, tr); err != nil {
		return nil, fmt.Errorf("writing the packed rootfs: %w", err)
	}
	return f, nil
}
