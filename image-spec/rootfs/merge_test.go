// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package rootfs

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"path"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The reference model applies the layers in order, as an unpacker does.

type rIno struct {
	typ          byte
	perm         int64
	uid, gid     int
	mtime        int64
	data         string
	target       string
	major, minor int64
	xattrs       map[string]string
	metaLayer    int // layer of the directory header that set the metadata
}

type rEnt struct {
	layer int
	ino   *rIno
}

type refState struct{ m map[string]*rEnt }

func newRef() *refState {
	return &refState{m: map[string]*rEnt{"": {layer: -1, ino: &rIno{typ: tar.TypeDir, perm: 0o755, metaLayer: -1}}}}
}

func (r *refState) isDir(p string) bool {
	e := r.m[p]
	return e != nil && e.ino.typ == tar.TypeDir
}

func (r *refState) removeAll(p string, below int) {
	for k, e := range r.m {
		if (k == p || strings.HasPrefix(k, p+"/")) && e.layer < below {
			delete(r.m, k)
		}
	}
}

// ensureDir makes the directories on the path, as an unpacker does. As in an
// overlay upper directory, these parents then belong to layer l.
func (r *refState) ensureDir(p string, l int) {
	if p == "" {
		return
	}
	cur := ""
	for c := range strings.SplitSeq(p, "/") {
		cur = path.Join(cur, c)
		if !r.isDir(cur) {
			delete(r.m, cur)
			r.m[cur] = &rEnt{layer: l, ino: &rIno{typ: tar.TypeDir, perm: 0o755, metaLayer: -1}}
		}
		r.m[cur].layer = l
	}
}

func setMeta(ino *rIno, h *tar.Header) {
	ino.uid, ino.gid, ino.mtime = h.Uid, h.Gid, h.ModTime.Unix()
	for k, v := range h.PAXRecords {
		if name, ok := strings.CutPrefix(k, "SCHILY.xattr."); ok {
			if ino.xattrs == nil {
				ino.xattrs = map[string]string{}
			}
			ino.xattrs[name] = v
		}
	}
}

func (r *refState) apply(h *tar.Header, data []byte, l int) {
	name := cleanName(h.Name)
	dir, base := splitName(name)
	if strings.HasPrefix(base, whPrefix) {
		r.ensureDir(dir, l)
		if base == whOpaque {
			for k, e := range r.m {
				if strings.HasPrefix(k, dir+"/") && dir != "" || dir == "" && k != "" {
					switch {
					case e.layer < l:
						delete(r.m, k)
					case e.ino.typ == tar.TypeDir && e.ino.metaLayer < l:
						// The lower directory is hidden, so its header does not apply.
						e.ino = &rIno{typ: tar.TypeDir, perm: 0o755, metaLayer: -1}
					}
				}
			}
			return
		}
		t := path.Join(dir, base[len(whPrefix):])
		if e := r.m[t]; e != nil && e.layer < l {
			r.removeAll(t, l)
		}
		return
	}
	if name == "" {
		e := r.m[""]
		e.ino.perm = h.Mode & 0o7777
		e.ino.metaLayer = l
		setMeta(e.ino, h)
		return
	}
	r.ensureDir(dir, l)
	if h.Typeflag == tar.TypeDir {
		if e := r.m[name]; e != nil && e.ino.typ == tar.TypeDir {
			e.ino.perm = h.Mode & 0o7777
			e.ino.metaLayer = l
			setMeta(e.ino, h)
			e.layer = l
			return
		}
		r.removeAll(name, 1<<30)
		ino := &rIno{typ: tar.TypeDir, perm: h.Mode & 0o7777, metaLayer: l}
		setMeta(ino, h)
		r.m[name] = &rEnt{layer: l, ino: ino}
		return
	}
	if h.Typeflag == tar.TypeLink {
		// As docker: link, lchown (removes the capability), setxattr, chmod
		// unless the target is a symlink, and chtimes.
		t := r.m[cleanName(h.Linkname)]
		r.removeAll(name, 1<<30)
		delete(t.ino.xattrs, "security.capability")
		setMeta(t.ino, h)
		if t.ino.typ != tar.TypeSymlink {
			t.ino.perm = h.Mode & 0o7777
		}
		r.m[name] = &rEnt{layer: l, ino: t.ino}
		return
	}
	r.removeAll(name, 1<<30)
	if h.Typeflag == tar.TypeChar && h.Devmajor == 0 && h.Devminor == 0 {
		return // overlay whiteout
	}
	ino := &rIno{
		typ: h.Typeflag, perm: h.Mode & 0o7777, data: string(data), target: h.Linkname,
		major: h.Devmajor, minor: h.Devminor,
	}
	if h.Typeflag != tar.TypeSymlink {
		ino.target = ""
	}
	setMeta(ino, h)
	r.m[name] = &rEnt{layer: l, ino: ino}
}

type rec struct {
	Typ          byte
	Perm         int64
	UID, GID     int
	Mtime        int64
	Data, Target string
	Major, Minor int64
	Xattrs       string
	Links        string
}

func xattrString(x map[string]string) string {
	keys := make([]string, 0, len(x))
	for k := range x {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%q;", k, x[k])
	}
	return b.String()
}

func linkGroups[K comparable](keys map[string]K) map[string]string {
	by := map[K][]string{}
	for p, k := range keys {
		by[k] = append(by[k], p)
	}
	out := map[string]string{}
	for _, ps := range by {
		sort.Strings(ps)
		if len(ps) > 1 {
			for _, p := range ps {
				out[p] = strings.Join(ps, ",")
			}
		}
	}
	return out
}

func refListing(r *refState) map[string]rec {
	out := map[string]rec{}
	keys := map[string]*rIno{}
	for p, e := range r.m {
		i := e.ino
		if i.typ != tar.TypeDir {
			keys[p] = i
		}
		out[p] = rec{
			Typ: i.typ, Perm: i.perm, UID: i.uid, GID: i.gid, Mtime: i.mtime, Data: i.data,
			Target: i.target, Major: i.major, Minor: i.minor, Xattrs: xattrString(i.xattrs),
		}
	}
	for p, g := range linkGroups(keys) {
		x := out[p]
		x.Links = g
		out[p] = x
	}
	return out
}

func typOf(m fs.FileMode) byte {
	switch m.Type() {
	case fs.ModeDir:
		return tar.TypeDir
	case fs.ModeSymlink:
		return tar.TypeSymlink
	case fs.ModeDevice | fs.ModeCharDevice:
		return tar.TypeChar
	case fs.ModeDevice:
		return tar.TypeBlock
	case fs.ModeNamedPipe:
		return tar.TypeFifo
	}
	return tar.TypeReg
}

func permOf(m fs.FileMode) int64 {
	p := int64(m.Perm())
	if m&fs.ModeSetuid != 0 {
		p |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		p |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		p |= 0o1000
	}
	return p
}

func fsListing(t *testing.T, f *FS) map[string]rec {
	out := map[string]rec{}
	keys := map[string]uint64{}
	err := fs.WalkDir(f, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, _ := f.Lstat(p)
		ino := fi.Sys().(*Inode)
		key := p
		if p == "." {
			key = ""
		}
		x := rec{
			Typ: typOf(fi.Mode()), Perm: permOf(fi.Mode()), UID: int(ino.UID), GID: int(ino.GID),
			Mtime: fi.ModTime().Unix(), Major: int64(ino.Major), Minor: int64(ino.Minor), Xattrs: xattrString(ino.Xattrs),
		}
		if fi.ModTime().IsZero() {
			x.Mtime = 0
		}
		switch x.Typ {
		case tar.TypeReg:
			b, err := fs.ReadFile(f, p)
			if err != nil {
				return err
			}
			x.Data = string(b)
			keys[key] = ino.Ino
			if int(ino.Nlink) < 1 {
				t.Errorf("%s: nlink %d", p, ino.Nlink)
			}
		case tar.TypeSymlink:
			x.Target, _ = f.ReadLink(p)
			keys[key] = ino.Ino
		case tar.TypeDir:
		default:
			keys[key] = ino.Ino
		}
		out[key] = x
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for p, g := range linkGroups(keys) {
		x := out[p]
		x.Links = g
		out[p] = x
	}
	return out
}

type ent struct {
	h    *tar.Header
	data []byte
}

func tarOf(t testing.TB, es []ent) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range es {
		h := *e.h
		h.Format = tar.FormatPAX
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(e.data))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func layersOf(t testing.TB, ls [][]ent) ([]Layer, *int) {
	opens := 0
	out := make([]Layer, len(ls))
	for i, es := range ls {
		b := tarOf(t, es)
		out[i] = func() (io.ReadCloser, error) {
			opens++
			return io.NopCloser(bytes.NewReader(b)), nil
		}
	}
	return out, &opens
}

func refOf(ls [][]ent) *refState {
	r := newRef()
	for l, es := range ls {
		for _, e := range es {
			r.apply(e.h, e.data, l)
		}
	}
	return r
}

// compare merges the layers, checks the result against the reference model
// and returns the merged tree with the number of layer opens.
func compare(t *testing.T, ls [][]ent, opts ...Option) (*FS, int) {
	t.Helper()
	layers, opens := layersOf(t, ls)
	f, err := Merge(context.Background(), layers, append([]Option{WithSpoolDir(t.TempDir())}, opts...)...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	got, want := fsListing(t, f), refListing(refOf(ls))
	for p, w := range want {
		if g, ok := got[p]; !ok {
			t.Errorf("missing %q: %+v", p, w)
		} else if g != w {
			t.Errorf("%q:\n got %+v\nwant %+v", p, g, w)
		}
	}
	for p, g := range got {
		if _, ok := want[p]; !ok {
			t.Errorf("extra %q: %+v", p, g)
		}
	}
	return f, *opens
}

var t0 = time.Unix(1700000000, 0)

func reg(name, data string) ent {
	return ent{h: &tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, ModTime: t0}, data: []byte(data)}
}

func dir(name string) ent {
	return ent{h: &tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o755, ModTime: t0}}
}

func link(name, target string) ent {
	return ent{h: &tar.Header{Typeflag: tar.TypeLink, Name: name, Linkname: target, Mode: 0o644, ModTime: t0}}
}

func sym(name, target string) ent {
	return ent{h: &tar.Header{Typeflag: tar.TypeSymlink, Name: name, Linkname: target, Mode: 0o777, ModTime: t0}}
}
func wh(name string) ent { d, b := splitName(name); return reg(path.Join(d, whPrefix+b), "") }
func opq(d string) ent   { return reg(path.Join(d, whOpaque), "") }

func TestOpaqueKeepsSameLayerSiblings(t *testing.T) {
	t.Parallel()

	f, _ := compare(t, [][]ent{
		{dir("d"), reg("d/old", "1"), reg("d/#early-old", "x")},
		{dir("d"), reg("d/#early", "2"), opq("d"), reg("d/new", "3")},
	})
	_, err := f.Lstat("d/#early")
	require.NoError(t, err)
	_, err = f.Lstat("d/old")
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestHardlinkTargetReplacedLater(t *testing.T) {
	t.Parallel()

	_, opens := compare(t, [][]ent{
		{reg("bin/a", "old"), link("bin/b", "bin/a"), link("bin/c", "bin/a")},
		{reg("bin/a", "new")},
		{wh("bin/c")},
	})
	// The hidden data of bin/a in layer 0 makes a second read of that layer.
	require.Equal(t, 4, opens)
}

func TestXattrsAndDevices(t *testing.T) {
	t.Parallel()

	ping := reg("usr/bin/ping", "elf")
	ping.h.PAXRecords = map[string]string{"SCHILY.xattr.security.capability": "\x01\x00\x00\x02"}
	f, _ := compare(t, [][]ent{{
		ping,
		{h: &tar.Header{Typeflag: tar.TypeChar, Name: "dev/null", Mode: 0o666, Devmajor: 1, Devminor: 3, ModTime: t0}},
		{h: &tar.Header{Typeflag: tar.TypeFifo, Name: "run/fifo", Mode: 0o600, ModTime: t0}},
		sym("bin", "usr/bin"),
	}})
	fi, err := f.Stat("bin/ping")
	require.NoError(t, err)
	require.Equal(t, "\x01\x00\x00\x02", fi.Sys().(*Inode).Xattrs["security.capability"])
}

func TestLinkHeaderMetadata(t *testing.T) {
	t.Parallel()

	exe := reg("x/cap", "elf")
	exe.h.Mode = 0o755
	exe.h.PAXRecords = map[string]string{"SCHILY.xattr.security.capability": "c"}
	f, _ := compare(t, [][]ent{{exe, link("x/cap-link", "x/cap")}})
	fi, err := f.Lstat("x/cap")
	require.NoError(t, err)
	require.Equal(t, fs.FileMode(0o644), fi.Mode().Perm())
	require.Nil(t, fi.Sys().(*Inode).Xattrs)
}

func TestDeviceWhiteout(t *testing.T) {
	t.Parallel()

	f, _ := compare(t, [][]ent{
		{reg("a/f", "1"), reg("a/g", "2")},
		{{h: &tar.Header{Typeflag: tar.TypeChar, Name: "a/f", Mode: 0o600, ModTime: t0}}},
	})
	_, err := f.Lstat("a/f")
	require.ErrorIs(t, err, fs.ErrNotExist)
}

var comps = []string{"a", "b", "c", "#x", "-y", "zz"}

func randPath(rng *rand.Rand) string {
	n := 1 + rng.Intn(3)
	ps := make([]string, n)
	for i := range ps {
		ps[i] = comps[rng.Intn(len(comps))]
	}
	return strings.Join(ps, "/")
}

func genLayers(rng *rand.Rand) [][]ent {
	r := newRef()
	nl := 1 + rng.Intn(5)
	var out [][]ent
	for l := range nl {
		var es []ent
		touched := map[string]bool{}
		add := func(e ent) {
			name := cleanName(e.h.Name)
			for p := name; p != "" && p != "."; p = path.Dir(p) {
				touched[p] = true
				if path.Dir(p) == "." {
					break
				}
			}
			r.apply(e.h, e.data, l)
			es = append(es, e)
		}
		meta := func(h *tar.Header) *tar.Header {
			h.Uid, h.Gid = rng.Intn(2)*1000, rng.Intn(2)*1000
			h.ModTime = t0.Add(time.Duration(rng.Intn(100)) * time.Second)
			return h
		}
		parents := func(p string) {
			if rng.Intn(2) == 0 {
				return
			}
			d := path.Dir(p)
			var ds []string
			for d != "." {
				ds = append([]string{d}, ds...)
				d = path.Dir(d)
			}
			for _, d := range ds {
				add(ent{h: meta(&tar.Header{Typeflag: tar.TypeDir, Name: d + "/", Mode: 0o700 + int64(rng.Intn(0o100))})})
			}
		}
		for range 1 + rng.Intn(12) {
			p := randPath(rng)
			switch op := rng.Intn(100); {
			case op < 35:
				parents(p)
				h := meta(&tar.Header{Typeflag: tar.TypeReg, Name: p, Mode: []int64{0o644, 0o755, 0o4755}[rng.Intn(3)]})
				if rng.Intn(4) == 0 {
					h.PAXRecords = map[string]string{"SCHILY.xattr.security.capability": fmt.Sprint(rng.Intn(9))}
					if rng.Intn(2) == 0 {
						h.PAXRecords["SCHILY.xattr.user.f"] = fmt.Sprint(rng.Intn(9))
					}
				}
				add(ent{h: h, data: []byte(fmt.Sprintf("L%d %s %d", l, p, rng.Intn(1000)))})
			case op < 50:
				parents(p)
				add(ent{h: meta(&tar.Header{Typeflag: tar.TypeDir, Name: p + "/", Mode: 0o750})})
			case op < 58:
				parents(p)
				add(ent{h: meta(&tar.Header{Typeflag: tar.TypeSymlink, Name: p, Linkname: randPath(rng), Mode: 0o777})})
			case op < 62:
				parents(p)
				add(ent{h: meta(&tar.Header{Typeflag: tar.TypeChar, Name: p, Mode: 0o666, Devmajor: int64(rng.Intn(2)), Devminor: int64(rng.Intn(2))})})
			case op < 80:
				var cands []string
				for k, e := range r.m {
					if k != "" && e.ino.typ != tar.TypeDir && k != p && !strings.HasPrefix(p, k+"/") && !strings.HasPrefix(k, p+"/") {
						cands = append(cands, k)
					}
				}
				if len(cands) == 0 {
					continue
				}
				slices.Sort(cands)
				tgt := cands[rng.Intn(len(cands))]
				ti := r.m[tgt].ino
				parents(p)
				if _, ok := r.m[tgt]; !ok || r.m[tgt].ino != ti {
					continue
				}
				h := &tar.Header{
					Typeflag: tar.TypeLink, Name: p, Linkname: tgt, Mode: ti.perm,
					Uid: ti.uid, Gid: ti.gid, ModTime: time.Unix(ti.mtime, 0),
				}
				if ti.xattrs != nil {
					h.PAXRecords = map[string]string{}
					for k, v := range ti.xattrs {
						h.PAXRecords["SCHILY.xattr."+k] = v
					}
				}
				if rng.Intn(2) == 0 {
					// A link header with its own metadata.
					meta(h)
					h.Mode = []int64{0o600, 0o755, 0o2755}[rng.Intn(3)]
					h.PAXRecords = nil
					if rng.Intn(3) == 0 {
						h.PAXRecords = map[string]string{"SCHILY.xattr.user.l": fmt.Sprint(rng.Intn(9))}
					}
				}
				add(ent{h: h})
			case op < 92:
				var cands []string
				for k, e := range r.m {
					if k != "" && e.layer < l && !touched[k] {
						cands = append(cands, k)
					}
				}
				slices.Sort(cands)
				if len(cands) == 0 || rng.Intn(8) == 0 {
					cands = append(cands, p)
				}
				tgt := cands[rng.Intn(len(cands))]
				bad := false
				for k := range touched {
					if k == tgt || strings.HasPrefix(k, tgt+"/") {
						bad = true
					}
				}
				if bad {
					continue
				}
				add(wh(tgt))
			default:
				var cands []string
				for k, e := range r.m {
					if k != "" && e.ino.typ == tar.TypeDir {
						cands = append(cands, k)
					}
				}
				if len(cands) == 0 {
					continue
				}
				slices.Sort(cands)
				add(opq(cands[rng.Intn(len(cands))]))
			}
		}
		out = append(out, es)
	}
	return out
}

func TestRandomAgainstReference(t *testing.T) {
	t.Parallel()

	n := int64(3000)
	if testing.Short() {
		n = 300
	}
	for seed := range n {
		ls := genLayers(rand.New(rand.NewSource(seed)))
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			compare(t, ls)
			if t.Failed() {
				for l, es := range ls {
					for _, e := range es {
						t.Logf("L%d %c %s -> %s %q mode=%o uid=%d mt=%d pax=%v", l, e.h.Typeflag, e.h.Name, e.h.Linkname, e.data, e.h.Mode, e.h.Uid, e.h.ModTime.Unix()%1000, e.h.PAXRecords)
					}
				}
				t.FailNow()
			}
		})
		if t.Failed() {
			return
		}
	}
}

func TestInMemoryMatchesSpool(t *testing.T) {
	t.Parallel()

	for seed := range int64(200) {
		ls := genLayers(rand.New(rand.NewSource(seed)))
		compare(t, ls, InMemory())
		compare(t, ls, WithAlign(4096))
	}
}
