// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Command packer packs a directory into a rootfs. It runs inside a build,
// where x/build mounts the rootfs it built and takes the packed result.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"unikraft.com/x/kraftfile"

	"unikraft.com/x/build/internal/buildfs"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "packer:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] != "pack" {
		return fmt.Errorf("usage: packer pack [flags] SRC OUT")
	}

	flags := flag.NewFlagSet("pack", flag.ContinueOnError)
	format := flags.String("format", string(kraftfile.FsTypeErofs), "rootfs format: erofs or cpio")
	pad := flags.Int64("pad", 0, "pad the output to a multiple of this many bytes")
	compress := flags.Bool("compress", false, "gzip a cpio rootfs")
	maxSize := flags.Int64("max-size", 0, "refuse a source of more than this many bytes")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return fmt.Errorf("usage: packer pack [flags] SRC OUT")
	}

	src := os.DirFS(flags.Arg(0))
	if err := buildfs.CheckSize(src, *maxSize); err != nil {
		return err
	}

	out, err := os.Create(flags.Arg(1))
	if err != nil {
		return err
	}
	if err := buildfs.Package(context.Background(), kraftfile.FsType(*format), out, src,
		buildfs.WithPad(*pad), buildfs.WithCompress(*compress)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
