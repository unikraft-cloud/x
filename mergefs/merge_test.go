// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package mergefs

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"math"
	"math/rand"
	"path"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

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
	xattrLayer   map[string]int // layer of the header that set each xattr
	metaLayer    int            // layer of the directory header that set the metadata
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

func (r *refState) removeAll(p string, before int) {
	for k, e := range r.m {
		if (k == p || below(k, p)) && e.layer < before {
			delete(r.m, k)
		}
	}
}

// allLayers makes removeAll remove the entries of every layer.
const allLayers = math.MaxInt

// tarPermBits are the permission, setuid, setgid and sticky bits of a tar mode.
const tarPermBits = 0o7777

// below reports if name is a path under dir, where "" is the root.
func below(name, dir string) bool {
	if dir == "" {
		return name != ""
	}
	return strings.HasPrefix(name, dir+"/")
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

func setMeta(ino *rIno, h *tar.Header, l int) {
	ino.uid, ino.gid, ino.mtime = h.Uid, h.Gid, h.ModTime.Unix()
	for k, v := range h.PAXRecords {
		if name, ok := strings.CutPrefix(k, "SCHILY.xattr."); ok {
			if ino.xattrs == nil {
				ino.xattrs, ino.xattrLayer = map[string]string{}, map[string]int{}
			}
			ino.xattrs[name], ino.xattrLayer[name] = v, l
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
				if below(k, dir) {
					switch {
					case e.layer < l:
						delete(r.m, k)
					case e.ino.typ == tar.TypeDir && e.ino.metaLayer < l:
						// The lower directory is hidden, so its header does not apply.
						e.ino = &rIno{typ: tar.TypeDir, perm: 0o755, metaLayer: -1}
					case e.ino.typ == tar.TypeDir:
						// As in an overlay, the xattrs of the hidden lower directory also do not apply.
						for x, xl := range e.ino.xattrLayer {
							if xl < l {
								delete(e.ino.xattrs, x)
								delete(e.ino.xattrLayer, x)
							}
						}
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
		e.ino.perm = h.Mode & tarPermBits
		e.ino.metaLayer = l
		setMeta(e.ino, h, l)
		return
	}
	r.ensureDir(dir, l)
	if h.Typeflag == tar.TypeDir {
		if e := r.m[name]; e != nil && e.ino.typ == tar.TypeDir {
			e.ino.perm = h.Mode & tarPermBits
			e.ino.metaLayer = l
			setMeta(e.ino, h, l)
			e.layer = l
			return
		}
		r.removeAll(name, allLayers)
		ino := &rIno{typ: tar.TypeDir, perm: h.Mode & tarPermBits, metaLayer: l}
		setMeta(ino, h, l)
		r.m[name] = &rEnt{layer: l, ino: ino}
		return
	}
	if h.Typeflag == tar.TypeLink {
		// As docker: link, lchown (removes the capability), setxattr, chmod
		// unless the target is a symlink, and chtimes.
		t := r.m[cleanName(h.Linkname)]
		r.removeAll(name, allLayers)
		delete(t.ino.xattrs, "security.capability")
		setMeta(t.ino, h, l)
		if t.ino.typ != tar.TypeSymlink {
			t.ino.perm = h.Mode & tarPermBits
		}
		r.m[name] = &rEnt{layer: l, ino: t.ino}
		return
	}
	r.removeAll(name, allLayers)
	if h.Typeflag == tar.TypeChar && h.Devmajor == 0 && h.Devminor == 0 {
		return // overlay whiteout
	}
	ino := &rIno{
		typ: h.Typeflag, perm: h.Mode & tarPermBits, data: string(data), target: h.Linkname,
		major: h.Devmajor, minor: h.Devminor,
	}
	if h.Typeflag != tar.TypeSymlink {
		ino.target = ""
	}
	setMeta(ino, h, l)
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

func tarTypeflag(m fs.FileMode) byte {
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

func tarMode(m fs.FileMode) int64 {
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
			Typ: tarTypeflag(fi.Mode()), Perm: tarMode(fi.Mode()), UID: int(ino.UID), GID: int(ino.GID),
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

func tarLayers(t testing.TB, ls [][]ent) ([]Layer, *int) {
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
// and that Merge opens each layer one time, and returns the merged tree.
func compare(t *testing.T, ls [][]ent, opts ...Option) *FS {
	t.Helper()
	layers, opens := tarLayers(t, ls)
	f, err := Merge(context.Background(), layers, opts...)
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
	if *opens != len(ls) {
		t.Errorf("%d opens of %d layers", *opens, len(ls))
	}
	return f
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

	f := compare(t, [][]ent{
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

	// The link bin/b needs the data of bin/a in layer 0, which layer 1 hides.
	f := compare(t, [][]ent{
		{reg("bin/a", "old"), link("bin/b", "bin/a"), link("bin/c", "bin/a")},
		{reg("bin/a", "new")},
		{wh("bin/c")},
	})
	got, err := fs.ReadFile(f, "bin/b")
	require.NoError(t, err)
	require.Equal(t, "old", string(got))
}

func TestXattrsAndDevices(t *testing.T) {
	t.Parallel()

	ping := reg("usr/bin/ping", "elf")
	ping.h.PAXRecords = map[string]string{"SCHILY.xattr.security.capability": "\x01\x00\x00\x02"}
	f := compare(t, [][]ent{{
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
	f := compare(t, [][]ent{{exe, link("x/cap-link", "x/cap")}})
	fi, err := f.Lstat("x/cap")
	require.NoError(t, err)
	require.Equal(t, fs.FileMode(0o644), fi.Mode().Perm())
	require.Nil(t, fi.Sys().(*Inode).Xattrs)
}

func TestReplacedDirXattrs(t *testing.T) {
	t.Parallel()

	d, sub := dir("d"), dir("d/sub")
	d.h.PAXRecords = map[string]string{"SCHILY.xattr.user.d": "1"}
	sub.h.PAXRecords = map[string]string{"SCHILY.xattr.user.sub": "1"}
	f := compare(t, [][]ent{
		{d, sub, reg("d", "file")},
		{dir("d"), dir("d/sub")},
	})
	for _, p := range []string{"d", "d/sub"} {
		fi, err := f.Lstat(p)
		require.NoError(t, err)
		require.Nil(t, fi.Sys().(*Inode).Xattrs, p)
	}
}

func TestDeviceWhiteout(t *testing.T) {
	t.Parallel()

	f := compare(t, [][]ent{
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
			if h.Typeflag == tar.TypeDir && rng.Intn(3) == 0 {
				h.PAXRecords = map[string]string{fmt.Sprintf("SCHILY.xattr.user.d%d", rng.Intn(2)): fmt.Sprint(l, rng.Intn(9))}
			}
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
	}
}

// byteSource is a rand.Source that takes its numbers from the fuzz input, so
// that the fuzzer steers genLayers. The numbers are 0 after the input.
type byteSource struct{ b []byte }

func (s *byteSource) Int63() int64 {
	var v uint32
	for range 4 {
		v <<= 8
		if len(s.b) > 0 {
			v |= uint32(s.b[0])
			s.b = s.b[1:]
		}
	}
	return int64(v) << 31
}

func (s *byteSource) Seed(int64) {}

func FuzzMergeAgainstReference(f *testing.F) {
	f.Add([]byte{})
	for seed := range int64(8) {
		b := make([]byte, 512)
		rand.New(rand.NewSource(seed)).Read(b)
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		compare(t, genLayers(rand.New(&byteSource{b: data})))
	})
}

func TestNameTooLong(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("n", maxComponent)
	deep := strings.Repeat("d/", maxName/2)
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{long, true},
		{"d/" + long, true},
		{path.Join("d", whPrefix+long), true},
		{long + "n", false},
		{"d/" + long + "n/f", false},
		{deep[:maxName-1], true},
		{deep + "f", false},
	} {
		layers, _ := tarLayers(t, [][]ent{{reg(tc.name, "x")}})
		f, err := Merge(context.Background(), layers)
		if tc.ok {
			require.NoError(t, err, len(tc.name))
			require.NoError(t, f.Close())
		} else {
			require.ErrorIs(t, err, ErrNameTooLong, len(tc.name))
		}
	}
}

func TestOpaqueBelowManyPendingLinks(t *testing.T) {
	t.Parallel()

	// Without the opaque whiteouts, the links find their targets.
	compare(t, opaqueBelowPendingLinks(200, false))

	// An opaque whiteout in each directory hides the targets, so the result has no links.
	layers, _ := tarLayers(t, opaqueBelowPendingLinks(200, true))
	f, err := Merge(context.Background(), layers, InMemory())
	require.NoError(t, err)
	defer f.Close()
	for i := range 200 {
		for _, name := range []string{fmt.Sprintf("d%d/l", i), fmt.Sprintf("d%d/t", i)} {
			_, err := f.Lstat(name)
			require.ErrorIs(t, err, fs.ErrNotExist, name)
		}
	}
}

// opaqueBelowPendingLinks returns the layers, from the bottom up, of n directories with a link in the top layer to a file of the bottom layer, and with opaque, an opaque whiteout in the middle layer.
func opaqueBelowPendingLinks(n int, opaque bool) [][]ent {
	var lower, middle, upper []ent
	for i := range n {
		d := fmt.Sprintf("d%d", i)
		lower = append(lower, reg(d+"/t", d))
		if opaque {
			middle = append(middle, opq(d))
		}
		upper = append(upper, link(d+"/l", d+"/t"))
	}
	return [][]ent{lower, middle, upper}
}

func TestMergeLimits(t *testing.T) {
	t.Parallel()

	layers, _ := tarLayers(t, [][]ent{{dir("d"), reg("d/a", "12345"), reg("d/b", "67890")}})
	for _, tc := range []struct {
		name string
		opts []Option
		err  error
	}{
		{"size at the limit", []Option{WithMaxSize(10)}, nil},
		{"size over the limit", []Option{WithMaxSize(9)}, ErrTooLarge},
		{"size over the limit in memory", []Option{WithMaxSize(9), InMemory()}, ErrTooLarge},
		{"entries at the limit", []Option{WithMaxEntries(3)}, nil},
		{"entries over the limit", []Option{WithMaxEntries(2)}, ErrTooManyEntries},
		{"implied directories at the limit", []Option{WithMaxEntries(5)}, nil},
		{"implied directories over the limit", []Option{WithMaxEntries(4)}, ErrTooManyEntries},
	} {
		t.Run(tc.name, func(t *testing.T) {
			layers := layers
			if strings.HasPrefix(tc.name, "implied") {
				// One entry that implies the directories a, a/b, a/b/c and a/b/c/d.
				layers, _ = tarLayers(t, [][]ent{{reg("a/b/c/d/f", "x")}})
			}
			f, err := Merge(t.Context(), layers, append(tc.opts, WithSpoolDir(t.TempDir()))...)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			require.NoError(t, f.Close())
		})
	}
}

func TestMergeLimitLinkToHiddenFile(t *testing.T) {
	t.Parallel()

	// The result has 10 bytes of data, as b links to the hidden a of layer 0 with 6 bytes.
	layers, _ := tarLayers(t, [][]ent{{reg("a", "123456"), link("b", "a")}, {reg("a", "7890")}})
	for _, tc := range []struct {
		name string
		opts []Option
		err  error
	}{
		{"at the limit", []Option{WithMaxSize(10)}, nil},
		{"at the limit in memory", []Option{WithMaxSize(10), InMemory()}, nil},
		{"over the limit", []Option{WithMaxSize(9)}, ErrTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := Merge(t.Context(), layers, tc.opts...)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			require.NoError(t, f.Close())
		})
	}
}

func TestMergeLimitHugeSparseFile(t *testing.T) {
	t.Parallel()

	// A sparse file can declare a size near MaxInt64 without data, and the sum with the stored data must not overflow.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "small", Mode: 0o644, Size: 10, Format: tar.FormatPAX}))
	_, err := tw.Write([]byte("0123456789"))
	require.NoError(t, err)
	require.NoError(t, tw.Flush())
	data := append(buf.Bytes(), sparseTar(t, "big", math.MaxInt64-5)...)
	base := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	upper, _ := tarLayers(t, [][]ent{{reg("big", "new")}})
	for _, tc := range []struct {
		name   string
		layers []Layer
	}{
		{"visible", []Layer{base}},
		{"hidden by an upper layer", []Layer{base, upper[0]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_, err := Merge(ctx, tc.layers, WithMaxSize(100), WithSpoolDir(t.TempDir()))
			require.ErrorIs(t, err, ErrTooLarge)
		})
	}
}

// withXattrs returns e with the xattrs.
func withXattrs(e ent, xs map[string]string) ent {
	e.h.PAXRecords = map[string]string{}
	for k, v := range xs {
		e.h.PAXRecords["SCHILY.xattr."+k] = v
	}
	return e
}

// manyXattrs returns n xattrs whose names start with prefix.
func manyXattrs(prefix string, n int) map[string]string {
	xs := map[string]string{}
	for i := range n {
		xs[fmt.Sprintf("user.%s%05d", prefix, i)] = "v"
	}
	return xs
}

func TestXattrTooLarge(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		layers [][]ent
		ok     bool
	}{
		{"largest name", [][]ent{{withXattrs(reg("f", ""), map[string]string{"user." + strings.Repeat("n", maxXattrName-5): "v"})}}, true},
		{"name too long", [][]ent{{withXattrs(reg("f", ""), map[string]string{"user." + strings.Repeat("n", maxXattrName-4): "v"})}}, false},
		{"largest value", [][]ent{{withXattrs(reg("f", ""), map[string]string{"user.a": strings.Repeat("v", maxXattrValue)})}}, true},
		{"value too large", [][]ent{{withXattrs(reg("f", ""), map[string]string{"user.a": strings.Repeat("v", maxXattrValue+1)})}}, false},
		{"names of one header", [][]ent{{withXattrs(dir("d"), manyXattrs("a", 6000))}}, false},
		{"names of one inode", [][]ent{{withXattrs(dir("d"), manyXattrs("a", 4000)), withXattrs(dir("d"), manyXattrs("b", 4000))}}, false},
		{"names of one inode in two layers", [][]ent{{withXattrs(dir("d"), manyXattrs("a", 4000))}, {withXattrs(dir("d"), manyXattrs("b", 4000))}}, false},
		{"same names again", [][]ent{{withXattrs(dir("d"), manyXattrs("a", 4000)), withXattrs(dir("d"), manyXattrs("a", 4000))}}, true},
		{"names of two links to a lower file", [][]ent{{reg("t", "")}, {withXattrs(link("a", "t"), manyXattrs("a", 4000)), withXattrs(link("b", "t"), manyXattrs("b", 4000))}}, false},
		{"same names on two links to a lower file", [][]ent{{reg("t", "")}, {withXattrs(link("a", "t"), manyXattrs("a", 4000)), withXattrs(link("b", "t"), manyXattrs("a", 4000))}}, true},
		{"names of a link and its lower target", [][]ent{{withXattrs(reg("t", ""), manyXattrs("a", 4000))}, {withXattrs(link("a", "t"), manyXattrs("b", 4000))}}, false},
		{"largest names and a link that drops the capability", [][]ent{{withXattrs(reg("t", ""), manyXattrs("a", maxXattrList/12))}, {link("a", "t")}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			layers, _ := tarLayers(t, tc.layers)
			f, err := Merge(context.Background(), layers, InMemory())
			if tc.ok {
				require.NoError(t, err)
				require.NoError(t, f.Close())
				return
			}
			require.ErrorIs(t, err, ErrXattrTooLarge)
		})
	}
}

func TestPurgeManyLowerEntries(t *testing.T) {
	t.Parallel()

	// Many directories in the top layer, and many entries that replace their parent in the layer below.
	compare(t, purgeLayers(500))
}

// purgeLayers returns the layers, from the bottom up, of n files d in the bottom layer and n directories below d in the top layer.
func purgeLayers(n int) [][]ent {
	var lower, upper []ent
	for i := range n {
		lower = append(lower, reg(fmt.Sprintf("x%d", i), ""), reg("d", fmt.Sprint(i)))
		upper = append(upper, dir(fmt.Sprintf("d/s%d", i)))
	}
	return [][]ent{lower, upper}
}

func TestPendingAboveManyEntries(t *testing.T) {
	t.Parallel()

	// Many links in the top layer wait for x, and the layer below has x many times.
	compare(t, pendingLayers(500))
}

// pendingLayers returns the layers, from the bottom up, of n entries x in the bottom layer and n links to x in the top layer.
func pendingLayers(n int) [][]ent {
	var lower, upper []ent
	for i := range n {
		lower = append(lower, reg("x", fmt.Sprint(i)))
		upper = append(upper, link(fmt.Sprintf("l%d", i), "x"))
	}
	return [][]ent{lower, upper}
}

func TestLinkTargetTooLong(t *testing.T) {
	t.Parallel()

	target := strings.Repeat("t", maxName)
	for _, tc := range []struct {
		e  ent
		ok bool
	}{
		{sym("s", target), true},
		{sym("s", target+"t"), false},
		{link("h", target+"t"), false},
	} {
		layers, _ := tarLayers(t, [][]ent{{tc.e}})
		f, err := Merge(context.Background(), layers, InMemory())
		if tc.ok {
			require.NoError(t, err, tc.e.h.Name)
			require.NoError(t, f.Close())
		} else {
			require.ErrorIs(t, err, ErrNameTooLong, tc.e.h.Name)
		}
	}
}

func TestHeaderStringsCopied(t *testing.T) {
	t.Parallel()

	// archive/tar returns the PAX records and a long link target as parts of one buffer.
	e := withXattrs(sym("s", strings.Repeat("t", 200)), map[string]string{"user.a": "v"})
	hdr, err := tar.NewReader(bytes.NewReader(tarOf(t, []ent{e}))).Next()
	require.NoError(t, err)

	h := keptHeader(hdr)
	require.False(t, inHeader(h.Linkname, hdr))
	for k, v := range h.PAXRecords {
		require.False(t, inHeader(k, hdr), k)
		require.False(t, inHeader(v, hdr), k)
	}

	ino := &Inode{rank: -1}
	require.NoError(t, new(merger).fill(ino, nil, hdr, 0))
	require.False(t, inHeader(ino.target, hdr))
	require.Len(t, ino.xs, 1)
	for _, x := range ino.xs {
		require.False(t, inHeader(x.k, hdr), x.k)
		require.False(t, inHeader(x.v, hdr), x.k)
	}
}

// inHeader reports if s shares memory with the link target or a PAX record of hdr.
func inHeader(s string, hdr *tar.Header) bool {
	p := uintptr(unsafe.Pointer(unsafe.StringData(s)))
	within := func(b string) bool {
		start := uintptr(unsafe.Pointer(unsafe.StringData(b)))
		return p >= start && p < start+uintptr(len(b))
	}
	if within(hdr.Linkname) {
		return true
	}
	for k, v := range hdr.PAXRecords {
		if within(k) || within(v) {
			return true
		}
	}
	return false
}

func TestKeptHeader(t *testing.T) {
	t.Parallel()

	hdr := &tar.Header{
		Typeflag: tar.TypeReg, Name: "f", Mode: 0o4750, Uid: 1, Gid: 2, Size: 3, ModTime: t0, Uname: "user",
		PAXRecords: map[string]string{"SCHILY.xattr.user.a": "1", "comment": strings.Repeat("c", 1<<20)},
	}
	h := keptHeader(hdr)
	require.Equal(t, map[string]string{"SCHILY.xattr.user.a": "1"}, h.PAXRecords)
	require.Equal(t, hdr.FileInfo().Mode(), h.FileInfo().Mode())
	require.Equal(t, []any{hdr.Uid, hdr.Gid, hdr.Size, hdr.ModTime}, []any{h.Uid, h.Gid, h.Size, h.ModTime})
	require.Empty(t, h.Uname)
}

func TestMergeLimitLinkTargets(t *testing.T) {
	t.Parallel()

	// The link targets count for WithMaxMetaSize: 100 bytes of symlink target, 100 of hard link target and 2 of names.
	target := strings.Repeat("t", 100)
	layers, _ := tarLayers(t, [][]ent{{sym("s", target), link("h", target)}})
	f, err := Merge(t.Context(), layers, WithMaxMetaSize(202), WithMaxSize(1), InMemory())
	require.NoError(t, err)
	require.NoError(t, f.Close())
	_, err = Merge(t.Context(), layers, WithMaxMetaSize(201), InMemory())
	require.ErrorIs(t, err, ErrMetaTooLarge)
}

func TestMergeLimitXattrs(t *testing.T) {
	t.Parallel()

	// The xattrs count for WithMaxMetaSize, and not for WithMaxSize: 6 bytes of xattr name, 100 bytes of value and 1 of file name.
	layers, _ := tarLayers(t, [][]ent{{withXattrs(reg("f", ""), map[string]string{"user.a": strings.Repeat("v", 100)})}})
	f, err := Merge(t.Context(), layers, WithMaxMetaSize(107), WithMaxSize(1), InMemory())
	require.NoError(t, err)
	require.NoError(t, f.Close())
	_, err = Merge(t.Context(), layers, WithMaxMetaSize(106), InMemory())
	require.ErrorIs(t, err, ErrMetaTooLarge)
}

func TestMergeLimitHiddenNames(t *testing.T) {
	t.Parallel()

	// The names of hidden entries count for WithMaxMetaSize: the upper layer hides the 100-byte name of layer 0.
	name := strings.Repeat("n", 100)
	layers, _ := tarLayers(t, [][]ent{{reg(name, "")}, {reg(name, "")}})
	f, err := Merge(t.Context(), layers, WithMaxMetaSize(200), InMemory())
	require.NoError(t, err)
	require.NoError(t, f.Close())
	_, err = Merge(t.Context(), layers, WithMaxMetaSize(199), InMemory())
	require.ErrorIs(t, err, ErrMetaTooLarge)
}

func TestMergeLimitImpliedPaths(t *testing.T) {
	t.Parallel()

	// The paths of the implied directories count for WithMaxMetaSize: 7 bytes of name, and 1, 3 and 5 for a, a/b and a/b/c.
	layers, _ := tarLayers(t, [][]ent{{reg("a/b/c/f", "")}})
	f, err := Merge(t.Context(), layers, WithMaxMetaSize(16), InMemory())
	require.NoError(t, err)
	require.NoError(t, f.Close())
	_, err = Merge(t.Context(), layers, WithMaxMetaSize(15), InMemory())
	require.ErrorIs(t, err, ErrMetaTooLarge)
}

func TestMergeLimitPendingTargets(t *testing.T) {
	t.Parallel()

	// The link of the upper layer waits for a target below 6 directories in a lower layer: with the root, 7 directories count.
	layers, _ := tarLayers(t, [][]ent{{reg("y", "")}, {link("l", "a/b/c/d/e/f/x")}})
	f, err := Merge(t.Context(), layers, WithMaxEntries(9), InMemory())
	require.NoError(t, err)
	require.NoError(t, f.Close())
	_, err = Merge(t.Context(), layers, WithMaxEntries(8), InMemory())
	require.ErrorIs(t, err, ErrTooManyEntries)
}

func TestMergeLimitRead(t *testing.T) {
	t.Parallel()

	skipped := ent{h: &tar.Header{Typeflag: 'Z', Name: "skipped", Size: 1 << 20, ModTime: t0}, data: make([]byte, 1<<20)}
	for _, tc := range []struct {
		name  string
		layer []byte
	}{
		{"zeros after the archive", append(tarOf(t, []ent{reg("f", "x")}), make([]byte, 1<<20)...)},
		{"data of a skipped entry", tarOf(t, []ent{skipped, reg("f", "x")})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			layer := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(tc.layer)), nil }
			f, err := Merge(t.Context(), []Layer{layer}, WithMaxReadSize(2<<20), InMemory())
			require.NoError(t, err)
			require.NoError(t, f.Close())
			_, err = Merge(t.Context(), []Layer{layer}, WithMaxReadSize(512<<10), InMemory())
			require.ErrorIs(t, err, ErrLayersTooLarge)
		})
	}

	t.Run("headers only", func(t *testing.T) {
		// 2048 directories are 1 MiB of headers, and the read stops at the limit.
		var ents []ent
		for i := range 2048 {
			ents = append(ents, dir(fmt.Sprintf("d%d", i)))
		}
		data := tarOf(t, ents)
		var read atomic.Int64
		layer := func() (io.ReadCloser, error) { return io.NopCloser(countReader{bytes.NewReader(data), &read}), nil }
		f, err := Merge(t.Context(), []Layer{layer}, WithMaxReadSize(2<<20), InMemory())
		require.NoError(t, err)
		require.NoError(t, f.Close())
		read.Store(0)
		_, err = Merge(t.Context(), []Layer{layer}, WithMaxReadSize(64<<10), InMemory())
		require.ErrorIs(t, err, ErrLayersTooLarge)
		require.LessOrEqual(t, read.Load(), int64(96<<10))
	})
}

// countReader adds the bytes that it reads from r to n.
type countReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}
