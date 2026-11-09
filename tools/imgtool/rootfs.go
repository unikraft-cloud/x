// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package main

import (
	"archive/tar"
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
	"time"

	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/unikraft/go-archivefs"
	"github.com/unikraft/go-archivefs/erofs"

	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/image-spec/reference"
)

type RootfsCmd struct {
	Image    string `arg:"" name:"image" help:"Image reference or path"`
	Output   string `arg:"" name:"output" help:"File to write the rootfs to"`
	Platform string `help:"Platform to load the image for" default:"kraftcloud/x86_64"`
	Tar      string `help:"Also write the tree of the rootfs to this tar file, for other tools such as mkfs.erofs"`

	Insecure string `help:"Allow insecure connections when accessing remote images" enum:"source,all,none" default:"none"`
}

// rootfsInfo is the JSON that the rootfs command prints.
type rootfsInfo struct {
	Image          string              `json:"image"`
	Manifest       string              `json:"manifest"`
	Platform       string              `json:"platform"`
	ImagePlatform  string              `json:"image_platform,omitempty"`
	Generic        bool                `json:"generic"`
	Config         ocispec.ImageConfig `json:"config"`
	Size           int64               `json:"size"`
	SHA256         string              `json:"sha256"`
	LoadSeconds    float64             `json:"load_seconds"`
	ConvertSeconds float64             `json:"convert_seconds"`
	WriteSeconds   float64             `json:"write_seconds"`
	Tree           *treeInfo           `json:"tree,omitempty"`
}

// treeInfo counts the entries of the tree that the tar file holds.
type treeInfo struct {
	Files     int   `json:"files"`
	Dirs      int   `json:"dirs"`
	Symlinks  int   `json:"symlinks"`
	Others    int   `json:"others"`
	HardLinks int   `json:"hard_links"`
	DataBytes int64 `json:"data_bytes"`
	// Sockets are not in the tar file, because tar cannot hold them.
	Sockets int `json:"sockets"`
}

func (c *RootfsCmd) Run(ctx context.Context) (rerr error) {
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
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), rc)
	if err = errors.Join(err, f.Close()); err != nil {
		return errors.Join(err, os.Remove(c.Output))
	}

	info := rootfsInfo{
		Image:          imageName(img),
		Manifest:       img.ManifestDescriptor.Digest.String(),
		Platform:       c.Platform,
		Generic:        img.Generic,
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
		tree, err := writeTar(c.Output, c.Tar)
		if err != nil {
			return fmt.Errorf("writing the tar file: %w", err)
		}
		info.Tree = &tree
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(info)
}

// imageName returns the reference of the image, pinned to the digest that it was loaded from.
func imageName(img *imagespec.Image) string {
	name := img.Reference.Format(reference.FormatOpts{OmitDigest: true})
	if img.Descriptor.Digest == "" {
		return name
	}
	if name != "" {
		name += "@"
	}
	return name + img.Descriptor.Digest.String()
}

// writeTar writes the tree of an EROFS image to a tar file, with owners, devices, extended attributes and hard links.
func writeTar(image, dst string) (info treeInfo, rerr error) {
	src, err := os.Open(image)
	if err != nil {
		return info, err
	}
	defer func() { rerr = errors.Join(rerr, src.Close()) }()
	fsys, err := erofs.Open(src)
	if err != nil {
		return info, fmt.Errorf("the rootfs is not an EROFS image: %w", err)
	}
	out, err := os.Create(dst)
	if err != nil {
		return info, err
	}
	defer func() {
		if rerr = errors.Join(rerr, out.Close()); rerr != nil {
			rerr = errors.Join(rerr, os.Remove(dst))
		}
	}()
	bw := bufio.NewWriterSize(out, 1<<20)
	tw := tar.NewWriter(bw)

	links := map[uint64]string{}
	err = fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		mode := fi.Mode()
		if mode&fs.ModeSocket != 0 {
			info.Sockets++
			return nil
		}
		var target string
		if mode&fs.ModeSymlink != 0 {
			if target, err = fsys.ReadLink(name); err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(fi, target)
		if err != nil {
			return err
		}
		sys := fi.Sys()
		hdr.Name, hdr.Uname, hdr.Gname, hdr.Format = name, "", "", tar.FormatPAX
		hdr.Uid, hdr.Gid = archivefs.GetUID(sys), archivefs.GetGID(sys)
		if mode&fs.ModeDevice != 0 {
			hdr.Devmajor, hdr.Devminor = int64(archivefs.GetDevMajor(sys)), int64(archivefs.GetDevMinor(sys))
		}
		xattrs, err := archivefs.GetXattrs(sys)
		if err != nil {
			return err
		}
		archivefs.SetTarXattrs(hdr, xattrs)

		if d.IsDir() {
			hdr.Name += "/"
			info.Dirs++
			return tw.WriteHeader(hdr)
		}
		if archivefs.GetNlink(sys) > 1 {
			ino := archivefs.GetIno(sys)
			if first, ok := links[ino]; ok {
				hdr.Typeflag, hdr.Linkname, hdr.Size = tar.TypeLink, first, 0
				info.HardLinks++
				return tw.WriteHeader(hdr)
			}
			links[ino] = name
		}
		switch {
		case mode.IsRegular():
			info.Files++
			info.DataBytes += fi.Size()
		case mode&fs.ModeSymlink != 0:
			info.Symlinks++
		default:
			info.Others++
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !mode.IsRegular() {
			return nil
		}
		f, err := fsys.Open(name)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		return errors.Join(err, f.Close())
	})
	if err != nil {
		return info, err
	}
	if err := tw.Close(); err != nil {
		return info, err
	}
	return info, bw.Flush()
}
