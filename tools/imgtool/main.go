// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/containerd/platforms"
	"github.com/distribution/reference"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/tonistiigi/units"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/log"
)

type CLI struct {
	Inspect InspectCmd `cmd:"" help:"Inspect an image"`
	Copy    CopyCmd    `cmd:"" help:"Copy an image"`
	Delete  DeleteCmd  `cmd:"" help:"Delete an image"`
}

type InspectCmd struct {
	Image string `arg:"" name:"image" help:"Image reference or path"`

	Insecure string `help:"Allow insecure connections when accessing remote images" enum:"source,all,none" default:"none"`
}

func (c *InspectCmd) Run(ctx context.Context) (rerr error) {
	insecure := c.Insecure == "source" || c.Insecure == "all"
	uri, err := imagespec.GuessURI(c.Image)
	if err != nil {
		return fmt.Errorf("parsing image reference: %w", err)
	}

	accessor := newAccessor(insecure)
	imgs, err := accessor.LoadAll(ctx, uri, platforms.All)
	if err != nil {
		return err
	}
	defer func() {
		for _, img := range imgs {
			if err := img.Close(); err != nil {
				rerr = errors.Join(rerr, err)
			}
		}
	}()

	w := os.Stdout

	for i, img := range imgs {
		if i > 0 {
			fmt.Fprintln(w, "\n"+strings.Repeat("-", 80)+"\n")
		}

		displayName := name(img.Name, img.Descriptor)
		if displayName == "" {
			displayName = c.Image
		}
		fmt.Fprintf(w, "Name: %s\n", displayName)
		mediaType := ""
		if img.Descriptor.MediaType != "" {
			mediaType = img.Descriptor.MediaType
		}
		fmt.Fprintf(w, "MediaType: %s\n", mediaType)
		fmt.Println()

		fmt.Fprintf(w, "Platform:\n")
		if img.Image != nil {
			if arch := img.Image.Architecture; arch != "" {
				if variant := img.Image.Variant; variant != "" {
					arch = path.Join(arch, variant)
				}
				fmt.Fprintf(w, "  Arch: %s\n", arch)
			}
			if os := img.Image.OS; os != "" {
				fmt.Fprintf(w, "  OS: %s\n", os)
			}
			if version := img.Image.OSVersion; version != "" {
				fmt.Fprintf(w, "  OS Version: %s\n", version)
			}
			if features := img.Image.OSFeatures; len(features) > 0 {
				fmt.Fprintf(w, "  OS Features: %v\n", features)
			}
		}
		fmt.Println()

		metadata := img.Metadata()
		fmt.Fprintf(w, "Metadata:\n")
		fmt.Fprintf(w, "  KraftKit: %s\n", metadata.KraftkitVersion)
		if metadata.Author != "" {
			fmt.Fprintf(w, "  Author: %s\n", metadata.Author)
		}
		if metadata.Created != nil {
			fmt.Fprintf(w, "  Created: %s\n", metadata.Created.Format("2006-01-02 15:04:05 MST"))
		}
		fmt.Println()

		if kernel := img.Kernel; kernel != nil {
			kernelDesc, _ := kernel.Source()
			fmt.Fprintf(w, "Kernel:\n")
			fmt.Fprintf(w, "  Name: %s\n", name(img.Name, kernelDesc))
			fmt.Fprintf(w, "  Path: %s\n", kernel.Path())
			fmt.Fprintf(w, "  Size: %.2f\n", units.Bytes(kernelDesc.Size))
		}
		if initrd := img.Initrd; initrd != nil {
			initrdDesc, _ := initrd.Source()
			fmt.Fprintf(w, "Initrd:\n")
			fmt.Fprintf(w, "  Name: %s\n", name(img.Name, initrdDesc))
			fmt.Fprintf(w, "  Path: %s\n", initrd.Path())
			fmt.Fprintf(w, "  Size: %.2f\n", units.Bytes(initrdDesc.Size))
		}
		if roms := img.Roms; len(roms) > 0 {
			fmt.Fprintf(w, "ROMs:\n")
			for _, rom := range roms {
				romDesc, _ := rom.Source()
				fmt.Fprintf(w, "  -  Name: %s\n", name(img.Name, romDesc))
				fmt.Fprintf(w, "     Path: %s\n", rom.Path())
				fmt.Fprintf(w, "     Size: %.2f\n", units.Bytes(romDesc.Size))
			}
		}
		fmt.Println()

		if imgConfig := img.Image; imgConfig != nil {
			fmt.Fprintf(w, "Runtime:\n")
			if len(imgConfig.Config.Entrypoint) > 0 {
				fmt.Fprintf(w, "  Entrypoint: %v\n", imgConfig.Config.Entrypoint)
			}
			if len(imgConfig.Config.Cmd) > 0 {
				fmt.Fprintf(w, "  Cmd: %v\n", imgConfig.Config.Cmd)
			}
			if len(imgConfig.Config.Env) > 0 {
				fmt.Fprintf(w, "  Env: %v\n", imgConfig.Config.Env)
			}
			if imgConfig.Config.User != "" {
				fmt.Fprintf(w, "  User: %s\n", imgConfig.Config.User)
			}
			if imgConfig.Config.WorkingDir != "" {
				fmt.Fprintf(w, "  WorkingDir: %s\n", imgConfig.Config.WorkingDir)
			}
		}
	}

	return nil
}

type CopyCmd struct {
	Source      string `arg:"" name:"source" help:"Source image reference or path"`
	Destination string `arg:"" name:"destination" help:"Destination image reference or path"`

	Insecure string `help:"Allow insecure connections when accessing remote images" enum:"source,destination,all,none" default:"none"`
}

func (c *CopyCmd) Run(ctx context.Context) (rerr error) {
	insecure := c.Insecure == "source" || c.Insecure == "all"
	src, err := imagespec.GuessURI(c.Source)
	if err != nil {
		return fmt.Errorf("parsing image source: %w", err)
	}
	dest, err := imagespec.GuessURI(c.Destination)
	if err != nil {
		return fmt.Errorf("parsing image destination: %w", err)
	}

	accessor := newAccessor(insecure)
	imgs, err := accessor.LoadAll(ctx, src, platforms.All)
	if err != nil {
		return err
	}
	defer func() {
		for _, img := range imgs {
			if err := img.Close(); err != nil {
				rerr = errors.Join(rerr, err)
			}
		}
	}()

	insecure = c.Insecure == "destination" || c.Insecure == "all"
	accessor = newAccessor(insecure)
	err = accessor.Save(ctx, dest, imgs...)
	if err != nil {
		return fmt.Errorf("saving image to destination: %w", err)
	}

	return nil
}

type DeleteCmd struct {
	Image string `arg:"" name:"image" help:"Image reference or path"`

	Insecure string `help:"Allow insecure connections when accessing remote images" enum:"source,all,none" default:"none"`
}

func (c *DeleteCmd) Run(ctx context.Context) error {
	insecure := c.Insecure == "source" || c.Insecure == "all"
	uri, err := imagespec.GuessURI(c.Image)
	if err != nil {
		return fmt.Errorf("parsing image reference: %w", err)
	}

	accessor := newAccessor(insecure)
	if err := accessor.Delete(ctx, uri); err != nil {
		return err
	}
	return nil
}

func name(named reference.Named, desc ocispec.Descriptor) string {
	result := ""
	if named != nil {
		result = reference.FamiliarString(named)
	}
	if desc.Digest != "" {
		if result != "" {
			result += "@"
		}
		result += desc.Digest.String()
	}
	return result
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	ctx = log.WithLogger(ctx, log.New(os.Stderr, log.TextType, log.DebugLevel))

	cli := &CLI{}
	kctx := kong.Parse(
		cli,
		kong.Name("imgtool"),
		kong.Description("Image tool"),
		kong.BindTo(ctx, (*context.Context)(nil)),
	)
	if err := kctx.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}
