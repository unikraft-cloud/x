// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/unikraft/go-archivefs/erofs"
	"github.com/unikraft/go-archivefs/tarfs"

	imagespec "unikraft.com/x/image-spec"
	xio "unikraft.com/x/io"
)

type RootfsCmd struct {
	Image    string `arg:"" name:"image" help:"Image reference or path"`
	Output   string `arg:"" name:"output" help:"File to write the rootfs to"`
	Platform string `help:"Platform to load the image for" default:"kraftcloud/x86_64"`
	Tar      string `help:"Also write the tree of the rootfs to this tar file, for tools such as mkfs.erofs; the initrd of the image must be EROFS"`

	Insecure string `help:"Allow insecure connections when accessing remote images" enum:"source,all,none" default:"none"`
}

// rootfsInfo is the JSON that the rootfs command prints.
type rootfsInfo struct {
	Image          string              `json:"image"`
	Manifest       string              `json:"manifest"`
	Platform       string              `json:"platform"`
	ImagePlatform  string              `json:"image_platform,omitempty"`
	Runtime        string              `json:"runtime"`
	Config         ocispec.ImageConfig `json:"config"`
	Size           int64               `json:"size"`
	SHA256         string              `json:"sha256"`
	LoadSeconds    float64             `json:"load_seconds"`
	ConvertSeconds float64             `json:"convert_seconds"`
	WriteSeconds   float64             `json:"write_seconds"`
}

func (c *RootfsCmd) Run(ctx context.Context) (rerr error) {
	var written []string
	defer func() {
		if rerr != nil {
			for _, p := range written {
				rerr = errors.Join(rerr, os.Remove(p))
			}
		}
	}()

	platform, err := platforms.Parse(c.Platform)
	if err != nil {
		return fmt.Errorf("parsing platform: %w", err)
	}
	loc, err := imagespec.GuessLocation(c.Image)
	if err != nil {
		return fmt.Errorf("parsing image reference: %w", err)
	}

	start := time.Now()
	accessor := newAccessor(c.Insecure == "source" || c.Insecure == "all")
	res, err := accessor.Resolve(ctx, loc)
	if err != nil {
		return err
	}
	img, err := accessor.Load(ctx, res, platforms.Only(platform))
	if err != nil {
		return err
	}
	defer func() { rerr = errors.Join(rerr, img.Close()) }()
	if img.Initrd == nil {
		if img.Runtime == imagespec.RuntimeKindDocker {
			return fmt.Errorf("a Docker image only has a rootfs for a Unikraft platform such as kraftcloud/x86_64, not %s", c.Platform)
		}
		return errors.New("the image has no initrd")
	}

	loaded := time.Now()
	rc, size, err := img.Initrd.Open(ctx)
	if err != nil {
		return err
	}
	converted := time.Now()
	defer func() { rerr = errors.Join(rerr, rc.Close()) }()
	f, err := os.Create(c.Output)
	if err != nil {
		return err
	}
	written = append(written, c.Output)
	h := sha256.New()
	_, err = io.Copy(xio.ContextWriter(ctx, io.MultiWriter(f, h)), rc)
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}

	info := rootfsInfo{
		Image:          name(img.Reference, img.Descriptor),
		Manifest:       img.ManifestDescriptor.Digest.String(),
		Platform:       c.Platform,
		Runtime:        img.Runtime.String(),
		Size:           size,
		SHA256:         hex.EncodeToString(h.Sum(nil)),
		LoadSeconds:    loaded.Sub(start).Seconds(),
		ConvertSeconds: converted.Sub(loaded).Seconds(),
		WriteSeconds:   time.Since(converted).Seconds(),
	}
	if img.Image != nil {
		info.ImagePlatform = platforms.Format(img.Image.Platform)
		info.Config = img.Image.Config
	}
	if c.Tar != "" {
		if err := writeTar(ctx, c.Output, c.Tar); err != nil {
			return fmt.Errorf("writing the tar file: %w", err)
		}
		written = append(written, c.Tar)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(info)
}

// writeTar writes the tree of an EROFS image to a tar file, with owners, devices, extended attributes and hard links.
func writeTar(ctx context.Context, image, dst string) (rerr error) {
	src, err := os.Open(image)
	if err != nil {
		return err
	}
	defer func() { rerr = errors.Join(rerr, src.Close()) }()
	fsys, err := erofs.Open(src)
	if err != nil {
		return fmt.Errorf("the rootfs is not an EROFS image: %w", err)
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		if rerr = errors.Join(rerr, out.Close()); rerr != nil {
			rerr = errors.Join(rerr, os.Remove(dst))
		}
	}()
	bw := bufio.NewWriterSize(xio.ContextWriter(ctx, out), 1<<20)
	if err := tarfs.Create(bw, fsys, tarfs.WithRoot(true)); err != nil {
		return err
	}
	return bw.Flush()
}
