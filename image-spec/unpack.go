// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sync/errgroup"
)

// TODO: implement Pack function to create an Image from a directory structure

func Unpack(ctx context.Context, img *Image, dest string) error {
	eg, ctx := errgroup.WithContext(ctx)
	if img.Kernel != nil {
		eg.Go(func() error {
			if err := unpackFile(ctx, img.Kernel, filepath.Join(dest, WellKnownKernelPath)); err != nil {
				return fmt.Errorf("unpacking kernel: %w", err)
			}
			return nil
		})
	}
	if img.Initrd != nil {
		eg.Go(func() error {
			if err := unpackFile(ctx, img.Initrd, filepath.Join(dest, WellKnownInitrdPath)); err != nil {
				return fmt.Errorf("unpacking initrd: %w", err)
			}
			return nil
		})
	}
	if len(img.Roms) > 0 {
		// NOTE: Right now we're baking in the assumption that there is only
		// ONE ROM layer within the image.  This is until there is better
		// coordination with the platform to handle multiple ROM layers.  For
		// now the assumption is that the ROM will land in the initrd path.
		rom := img.Roms[len(img.Roms)-1]
		eg.Go(func() error {
			if err := unpackFile(ctx, rom, filepath.Join(dest, WellKnownRomPath)); err != nil {
				return fmt.Errorf("unpacking rom: %w", err)
			}
			return nil
		})
	}
	if img.Image != nil {
		eg.Go(func() error {
			if err := unpackJSON(img.Image, filepath.Join(dest, WellKnownConfigPath)); err != nil {
				return fmt.Errorf("unpacking config.json: %w", err)
			}
			return nil
		})

		// Deprecated: cmdline.txt is deprecated in favor of config.json
		eg.Go(func() error {
			if cmd := img.Image.Config.Cmd; len(cmd) > 0 && cmd[0] != "--" {
				if err := unpackData([]byte(strings.Join(cmd, " ")), filepath.Join(dest, WellKnownCmdlinePath)); err != nil {
					return fmt.Errorf("unpacking cmdline.txt: %w", err)
				}
			}
			return nil
		})
	}

	return eg.Wait()
}

func unpackData(dt []byte, destPath string) error {
	err := os.MkdirAll(filepath.Dir(destPath), 0o755)
	if err != nil {
		return err
	}
	return os.WriteFile(destPath, dt, 0o644)
}

func unpackJSON(dt any, destPath string) error {
	err := os.MkdirAll(filepath.Dir(destPath), 0o755)
	if err != nil {
		return err
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	err = enc.Encode(dt)
	if err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func unpackFile(ctx context.Context, file File, destPath string) error {
	// TODO: would be *nice* to do hardlinking, but sadly containerd local
	// stores don't let us get the path to the file on disk

	f, _, err := file.Open(ctx)
	if err != nil {
		return err
	}
	defer f.Close()

	err = os.MkdirAll(filepath.Dir(destPath), 0o755)
	if err != nil {
		return err
	}
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}

	_, err = io.Copy(out, f)
	if err != nil {
		out.Close()
		return err
	}

	return out.Close()
}
