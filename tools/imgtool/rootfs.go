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
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/unikraft/go-archivefs/cpio"
	"github.com/unikraft/go-archivefs/erofs"
	"github.com/unikraft/go-archivefs/tarfs"

	imagespec "unikraft.com/x/image-spec"
	xio "unikraft.com/x/io"
	"unikraft.com/x/mergefs"
)

type RootfsCmd struct {
	Image    string `arg:"" name:"image" help:"Image reference or path"`
	Output   string `arg:"" name:"output" help:"File to write the rootfs to"`
	Platform string `help:"Platform to load the image for" default:"kraftcloud/x86_64"`
	Tar      string `help:"Also write the tree of the rootfs to this tar file, for tools such as mkfs.erofs"`
	Format   string `help:"Format of the rootfs: erofs or cpio, by default the format of the initrd; the tool converts an EROFS initrd to CPIO, but not a CPIO initrd to EROFS" enum:",erofs,cpio" default:""`

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
	Format         string              `json:"format,omitempty"`
	CPIOSeconds    float64             `json:"cpio_seconds,omitempty"`
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
	var loadOpts []imagespec.LoadOpt
	if c.Format == "cpio" {
		loadOpts = append(loadOpts, imagespec.WithRootfs("", func(ctx context.Context, dst *os.File, layers []mergefs.Layer, opts ...mergefs.Option) error {
			return mergefs.CPIO(ctx, dst, layers, opts...)
		}))
	}
	img, err := accessor.Load(ctx, res, platforms.Only(platform), loadOpts...)
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
	format := rootfsFormat(c.Output)
	switch {
	case format == "" && (c.Format != "" || c.Tar != ""):
		return errors.New("the initrd is not an EROFS image or an uncompressed CPIO archive")
	case format == "cpio" && c.Format == "erofs":
		return errors.New("the initrd is a CPIO archive, which the tool cannot convert to EROFS")
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
		Format:         format,
	}
	if img.Image != nil {
		info.ImagePlatform = platforms.Format(img.Image.Platform)
		info.Config = img.Image.Config
	}
	if c.Tar != "" {
		if err := writeTar(ctx, c.Output, format, c.Tar); err != nil {
			return fmt.Errorf("writing the tar file: %w", err)
		}
		written = append(written, c.Tar)
	}
	if format == "erofs" && c.Format == "cpio" {
		start := time.Now()
		if info.Size, info.SHA256, err = toCPIO(ctx, c.Output); err != nil {
			return fmt.Errorf("converting the rootfs to CPIO: %w", err)
		}
		info.Format = c.Format
		info.CPIOSeconds = time.Since(start).Seconds()
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(info)
}

// rootfsFormat returns the format of the rootfs at path from its magic bytes: erofs, cpio, or "" for another format.
func rootfsFormat(path string) string {
	switch {
	case erofs.IsValidPath(path):
		return "erofs"
	case cpio.IsValidPath(path):
		return "cpio"
	}
	return ""
}

// toCPIO replaces the EROFS image at path with a CPIO archive of its tree, and returns its size and SHA-256.
func toCPIO(ctx context.Context, path string) (size int64, sum string, rerr error) {
	src, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer func() { rerr = errors.Join(rerr, src.Close()) }()
	fsys, err := erofs.Open(src)
	if err != nil {
		return 0, "", fmt.Errorf("the rootfs is not an EROFS image: %w", err)
	}
	out, err := os.CreateTemp(filepath.Dir(path), ".imgtool-rootfs-*.cpio")
	if err != nil {
		return 0, "", err
	}
	defer func() {
		if rerr != nil {
			rerr = errors.Join(rerr, os.Remove(out.Name()))
		}
	}()
	h := sha256.New()
	err = cpio.Create(xio.ContextWriter(ctx, io.MultiWriter(out, h)), fsys)
	if err == nil {
		size, err = out.Seek(0, io.SeekCurrent)
	}
	if err = errors.Join(err, out.Chmod(0o644), out.Close()); err != nil {
		return 0, "", err
	}
	if err := os.Rename(out.Name(), path); err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(h.Sum(nil)), nil
}

// writeTar writes the tree of a rootfs in the format to a tar file, with owners, devices, extended attributes and hard links.
func writeTar(ctx context.Context, image, format, dst string) (rerr error) {
	src, err := os.Open(image)
	if err != nil {
		return err
	}
	defer func() { rerr = errors.Join(rerr, src.Close()) }()
	var fsys fs.ReadLinkFS
	switch format {
	case "cpio":
		fsys, err = cpio.OpenFS(src)
	case "erofs":
		fsys, err = erofs.Open(src)
	default:
		return fmt.Errorf("unsupported rootfs format %q", format)
	}
	if err != nil {
		return err
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
