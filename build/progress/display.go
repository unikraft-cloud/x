// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package progress

import (
	"context"
	"errors"
	"io"

	"github.com/moby/buildkit/client"
	// Registers the error detail types buildkitd attaches to solve errors.
	_ "github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/util/progress/progressui"
)

// Display renders the progress of a build through progressui, the renderer
// of buildctl and buildx: live in a terminal, as plain text, or as rawjson.
type Display struct {
	statuses chan *client.SolveStatus
	done     chan struct{}
	err      error
}

// NewDisplay starts a display that renders to the writer out, in one of
// progressui's modes: auto draws a terminal live and falls back to plain
// text for anything else, and plain renders the way
// `docker buildx build --progress=plain` does.
func NewDisplay(ctx context.Context, out io.Writer, mode progressui.DisplayMode) (*Display, error) {
	disp, err := progressui.NewDisplay(out, mode)
	if err != nil {
		return nil, err
	}

	d := &Display{
		statuses: make(chan *client.SolveStatus),
		done:     make(chan struct{}),
	}
	go func() {
		defer close(d.done)
		// Outlives ctx so that a cancelled build still shows how it ended.
		_, err := disp.UpdateFrom(context.WithoutCancel(ctx), d.statuses)
		if err == nil {
			err = context.Cause(ctx)
		}
		d.err = err
	}()

	return d, nil
}

// Write renders ss.
func (d *Display) Write(ss *client.SolveStatus) {
	d.statuses <- ss
}

// Close ends the display once, after the last Write, and waits for it to
// stop.
func (d *Display) Close() error {
	close(d.statuses)
	<-d.done
	return d.err
}

// Plain renders the statuses in r, one per line as an Encoder writes them,
// to out the way `docker buildx build --progress=plain` does, until r ends
// or ctx is done. A read blocked on r only ends when r does, so give a
// reader that ends with ctx, such as the body of a request made with it.
func Plain(ctx context.Context, out io.Writer, r io.Reader) error {
	d, err := NewDisplay(ctx, out, progressui.PlainMode)
	if err != nil {
		return err
	}

	dec := NewDecoder(r)
	for {
		ss, err := dec.Decode()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.Join(err, d.Close())
		}
		if ctx.Err() != nil {
			return errors.Join(context.Cause(ctx), d.Close())
		}
		d.Write(ss)
	}

	return d.Close()
}
