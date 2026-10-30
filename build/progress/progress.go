// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package progress reports every step of a build as a buildkit SolveStatus,
// the steps outside the daemon included, so that one renderer draws the
// whole build and one wire format carries it.
package progress

import (
	"strings"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/solver/pb"
	"github.com/opencontainers/go-digest"
)

// Writer takes the progress of a build, one status at a time, in the order
// the build reports it.
type Writer interface {
	Write(*client.SolveStatus)
}

// WriterFunc is a Writer made of a function.
type WriterFunc func(*client.SolveStatus)

// Write calls f.
func (f WriterFunc) Write(ss *client.SolveStatus) { f(ss) }

// Discard drops every status.
var Discard Writer = WriterFunc(func(*client.SolveStatus) {})

// WithPrefix reports to w with prefix in the name and the digest of every
// vertex, so that the same vertex in two solves is drawn twice.
func WithPrefix(w Writer, prefix string) Writer {
	under := func(d digest.Digest) digest.Digest {
		if d == "" {
			return d
		}
		return digest.FromString(prefix + " " + d.String())
	}

	return WriterFunc(func(ss *client.SolveStatus) {
		var prefixed client.SolveStatus
		for _, v := range ss.Vertexes {
			pv := *v
			pv.Digest = under(v.Digest)
			pv.Inputs = make([]digest.Digest, len(v.Inputs))
			for i, in := range v.Inputs {
				pv.Inputs[i] = under(in)
			}
			pv.Name = "[" + prefix + "] " + v.Name
			if rest, ok := strings.CutPrefix(v.Name, "["); ok {
				pv.Name = "[" + prefix + " " + rest
			}
			if pg := v.ProgressGroup; pg != nil {
				pv.ProgressGroup = &pb.ProgressGroup{Id: prefix + " " + pg.Id, Name: pg.Name, Weak: pg.Weak}
			}
			prefixed.Vertexes = append(prefixed.Vertexes, &pv)
		}
		for _, s := range ss.Statuses {
			ps := *s
			ps.Vertex = under(s.Vertex)
			prefixed.Statuses = append(prefixed.Statuses, &ps)
		}
		for _, l := range ss.Logs {
			pl := *l
			pl.Vertex = under(l.Vertex)
			prefixed.Logs = append(prefixed.Logs, &pl)
		}
		for _, wn := range ss.Warnings {
			pw := *wn
			pw.Vertex = under(wn.Vertex)
			prefixed.Warnings = append(prefixed.Warnings, &pw)
		}
		w.Write(&prefixed)
	})
}

// Step runs fn and reports it to w as a vertex of its own
func Step(w Writer, name string, fn func() error) error {
	started := client.Vertex{
		Digest:  digest.FromString(identity.NewID()),
		Name:    name,
		Started: new(time.Now()),
	}
	w.Write(&client.SolveStatus{Vertexes: []*client.Vertex{&started}})

	err := fn()

	completed := started
	completed.Completed = new(time.Now())
	if err != nil {
		completed.Error = err.Error()
	}
	w.Write(&client.SolveStatus{Vertexes: []*client.Vertex{&completed}})

	return err
}

// NewChannel returns a channel to give buildkit's client for one solve, and
// a channel that closes once every status sent on the first has reached w.
func NewChannel(w Writer) (statuses chan *client.SolveStatus, done <-chan struct{}) {
	ch := make(chan *client.SolveStatus)
	d := make(chan struct{})
	go func() {
		defer close(d)
		for ss := range ch {
			w.Write(ss)
		}
	}()
	return ch, d
}
