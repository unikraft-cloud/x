// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package rootfs

import (
	"archive/tar"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
)

// The merge reads the top layer first. Entry i of layer l has the rank (l, i),
// and a node of a higher layer or an opaque rank above (l, i) hides it.

const (
	whPrefix = ".wh."
	whOpaque = ".wh..wh..opq"
	capXattr = "security.capability"
)

const (
	kDir uint8 = iota
	kOther
	kWhiteout
)

const (
	stResolved uint8 = iota
	stPending
	stRefetch
	stDangling
)

type node struct {
	name   string
	parent *node
	kids   map[string]*node // during the merge
	list   []*node          // sorted, after the merge
	ino    *Inode
	layer  int32
	idx    int32 // entry index in the layer that made the node
	opaque int64 // hide entries with a rank below this rank
	cut    int64 // ignore own directory headers with a rank below this rank
	kind   uint8
}

type pend struct {
	ino      *Inode
	layer    int32 // entries of layers below this layer resolve it
	metaOnly bool  // only a hidden link waits, so the data is not necessary
}

// seenRec is a hidden entry of the current layer. For a hard link, it keeps
// the target of the link at the time that the merge read it.
type seenRec struct {
	index  int32
	isLink bool
	dead   bool     // link without a valid target
	queued bool     // second read is in the queue
	target string   // link target, for a lookup in a lower layer
	via    *seenRec // link to a different hidden entry
	ino    *Inode   // inode of the entry, made on first use
	pend   *pend
}

type merger struct {
	layers   []Layer
	opts     options
	root     *node
	spoolF   *os.File
	ownSpool bool
	spoolW   *bufio.Writer
	spoolOff int64
	cpBuf    []byte
	pending  map[string][]*pend
	seen     map[string]*seenRec
	cands    map[string]*Inode
	refetch  map[int32][]*Inode
}

func newMerger(layers []Layer, o options) (*merger, error) {
	m := &merger{
		layers:  layers,
		opts:    o,
		pending: map[string][]*pend{},
		refetch: map[int32][]*Inode{},
		cpBuf:   make([]byte, 1<<20),
	}
	m.root = &node{
		name: ".", kind: kDir, opaque: -1, cut: -1,
		ino: &Inode{mode: fs.ModeDir | 0o755, rank: -1},
	}
	if o.inMemory {
		return m, nil
	}
	if o.spoolFile != nil {
		m.spoolF = o.spoolFile
	} else {
		f, err := os.CreateTemp(o.spoolDir, "rootfs-spool-*")
		if err != nil {
			return nil, err
		}
		_ = os.Remove(f.Name())
		m.spoolF, m.ownSpool = f, true
	}
	m.spoolW = bufio.NewWriterSize(onlyWriter{m.spoolF}, 1<<20)
	return m, nil
}

func (m *merger) close() {
	if m.spoolF != nil && m.ownSpool {
		_ = m.spoolF.Close()
	}
}

func (m *merger) run(ctx context.Context) error {
	for l := range slices.Backward(m.layers) {
		if err := m.readLayer(ctx, int32(l)); err != nil {
			return fmt.Errorf("layer %d: %w", l, err)
		}
	}
	m.seen = nil
	layers := make([]int32, 0, len(m.refetch))
	for l := range m.refetch {
		layers = append(layers, l)
	}
	slices.Sort(layers)
	for _, l := range layers {
		if err := m.refetchLayer(l, m.refetch[l]); err != nil {
			return fmt.Errorf("refetch layer %d: %w", l, err)
		}
	}
	for _, ps := range m.pending {
		for _, p := range ps {
			find(p.ino).state = stDangling
		}
	}
	m.pending = nil
	if m.spoolW != nil {
		if err := m.spoolW.Flush(); err != nil {
			return err
		}
	}
	m.finalize()
	return nil
}

// onlyWriter hides io.ReaderFrom, so that copies use our buffer.
type onlyWriter struct{ io.Writer }

func (m *merger) open(l int32) (*tar.Reader, io.ReadCloser, error) {
	rc, err := m.layers[l]()
	if err != nil {
		return nil, nil, err
	}
	return tar.NewReader(rc), rc, nil
}

// finish reads the stream to the end, so that a verifying reader sees EOF.
func (m *merger) finish(rc io.ReadCloser) error {
	_, err := io.CopyBuffer(io.Discard, rc, m.cpBuf)
	if cerr := rc.Close(); err == nil {
		err = cerr
	}
	return err
}

func (m *merger) readLayer(ctx context.Context, l int32) error {
	tr, rc, err := m.open(l)
	if err != nil {
		return err
	}
	m.seen = map[string]*seenRec{}
	m.cands = map[string]*Inode{}
	for i := int32(0); ; i++ {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = rc.Close()
			return err
		}
		if i&1023 == 0 {
			if err := ctx.Err(); err != nil {
				_ = rc.Close()
				return err
			}
		}
		if err := m.entry(tr, hdr, l, i); err != nil {
			_ = rc.Close()
			return fmt.Errorf("%s: %w", hdr.Name, err)
		}
	}
	m.resolvePending(l)
	return m.finish(rc)
}

// cleanName makes a tar name relative to the root. The clean of a rooted
// path removes "..", so that names stay in the root.
func cleanName(s string) string { return path.Clean("/" + s)[1:] }

func splitName(s string) (dir, base string) {
	dir, base, ok := strings.CutLast(s, "/")
	if !ok {
		return "", s
	}
	return dir, base
}

func joinName(dir, base string) string {
	if dir == "" {
		return base
	}
	return dir + "/" + base
}

func rankOf(l, i int32) int64 { return int64(l)<<32 | int64(uint32(i)) }

func isDirHdr(h *tar.Header) bool { return h.Typeflag == tar.TypeDir }

// isDevWhiteout reports an overlay whiteout: a char device 0/0.
func isDevWhiteout(h *tar.Header) bool {
	return h.Typeflag == tar.TypeChar && h.Devmajor == 0 && h.Devminor == 0
}

func (m *merger) entry(tr *tar.Reader, hdr *tar.Header, l, i int32) error {
	switch hdr.Typeflag {
	case tar.TypeReg, tar.TypeCont, tar.TypeGNUSparse, tar.TypeLink,
		tar.TypeSymlink, tar.TypeChar, tar.TypeBlock, tar.TypeDir, tar.TypeFifo:
	default:
		return nil
	}
	name := cleanName(hdr.Name)
	rank := rankOf(l, i)
	if name == "" {
		if isDirHdr(hdr) {
			m.applyDir(m.root, hdr, rank)
		}
		return nil
	}
	dir, base := splitName(name)
	if strings.HasPrefix(base, whPrefix) {
		if base == whOpaque {
			if d := m.dirFor(dir, l, i); d != nil {
				d.opaque = max(d.opaque, rankOf(l, 0))
			}
			return nil
		}
		m.whiteout(dir, base[len(whPrefix):], l, i)
		return nil
	}

	var vis *node
	visible := false
	replace := false
	parent := m.dirFor(dir, l, i)
	if parent != nil {
		ex := parent.kids[base]
		switch {
		case ex == nil:
			visible = true
		case ex.kind == kDir:
			if isDirHdr(hdr) {
				m.applyDir(ex, hdr, rank)
				break
			}
			if ex.layer > l {
				// The entry replaced the directory in this layer. Remove what
				// earlier entries of this layer put there, and hide lower layers.
				m.purge(ex, l)
				ex.opaque = max(ex.opaque, rank)
				ex.cut = max(ex.cut, rank)
				break
			}
			delete(parent.kids, ex.name)
			visible, replace = true, true
		case ex.layer > l:
		default:
			delete(parent.kids, ex.name)
			visible, replace = true, true
		}
	}

	if visible {
		var err error
		vis, err = m.create(tr, parent, base, hdr, l, i, replace)
		if err != nil {
			return err
		}
	} else if !isDirHdr(hdr) {
		rec := &seenRec{index: i}
		if hdr.Typeflag == tar.TypeLink {
			*rec = m.linkRef(cleanName(hdr.Linkname), l)
			rec.index = i
			// A hidden link also did chown, chmod and setxattr on its target.
			if ino := m.recIno(rec, l); ino != nil {
				applyMeta(ino, hdr, rank, true)
			}
		}
		if isDevWhiteout(hdr) {
			rec.dead = true
		}
		m.seen[name] = rec
	}

	if len(m.pending) > 0 {
		if need, ok := m.pendingAbove(name, l); ok {
			c, err := m.candidate(tr, hdr, name, l, i, vis, need)
			if err != nil {
				return err
			}
			m.cands[name] = c
		}
	}
	return nil
}

// dirFor returns the directory at dir for entry i of layer l, and makes the
// missing directories. It returns nil when an upper layer hides the path.
func (m *merger) dirFor(dir string, l, i int32) *node {
	rank := rankOf(l, i)
	n := m.root
	for dir != "" {
		if n.opaque > rank {
			return nil
		}
		var comp string
		if j := strings.IndexByte(dir, '/'); j >= 0 {
			comp, dir = dir[:j], dir[j+1:]
		} else {
			comp, dir = dir, ""
		}
		c := n.kids[comp]
		switch {
		case c == nil:
			c = m.newDir(n, strings.Clone(comp), l)
		case c.kind != kDir:
			if c.layer > l {
				return nil
			}
			delete(n.kids, c.name)
			c = m.newDir(n, c.name, l)
			c.opaque, c.cut = rank, rank
		}
		n = c
	}
	if n.opaque > rank {
		return nil
	}
	return n
}

func (m *merger) newDir(parent *node, name string, l int32) *node {
	d := &node{
		name: name, parent: parent, kind: kDir, layer: l, opaque: -1, cut: -1,
		ino: &Inode{mode: fs.ModeDir | 0o755, rank: -1},
	}
	parent.add(d)
	return d
}

func (n *node) add(c *node) {
	if n.kids == nil {
		n.kids = map[string]*node{}
	}
	c.parent = n
	n.kids[c.name] = c
}

// purge removes from the subtree of d the entries that layer l added.
func (m *merger) purge(d *node, l int32) {
	if d.ino.rank >= 0 && int32(d.ino.rank>>32) == l {
		*d.ino = Inode{mode: fs.ModeDir | 0o755, rank: -1}
	}
	for name, c := range d.kids {
		switch {
		case c.layer == l:
			delete(d.kids, name)
		case c.kind == kDir:
			m.purge(c, l)
		}
	}
}

func (m *merger) whiteout(dir, name string, l, i int32) {
	if name == "" {
		return
	}
	if target := joinName(dir, name); len(m.pending) > 0 {
		if _, ok := m.pendingAbove(target, l); ok {
			if _, ok := m.cands[target]; !ok {
				m.cands[target] = nil
			}
		}
	}
	parent := m.dirFor(dir, l, i)
	if parent == nil {
		return
	}
	ex := parent.kids[name]
	switch {
	case ex == nil:
		parent.add(&node{name: strings.Clone(name), kind: kWhiteout, layer: l, idx: i})
	case ex.kind == kDir:
		ex.opaque = max(ex.opaque, rankOf(l, 0))
		ex.cut = max(ex.cut, rankOf(l, 0))
	}
}

const permBits = fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky

func (m *merger) applyDir(n *node, hdr *tar.Header, rank int64) {
	if rank < n.cut {
		return
	}
	if rank > n.ino.rank {
		n.ino.mode = fs.ModeDir | hdr.FileInfo().Mode()&permBits
	}
	addXattrs(n.ino, hdr, rank, false)
	if rank > n.ino.rank {
		n.ino.UID, n.ino.GID, n.ino.mtime, n.ino.rank = uint32(hdr.Uid), uint32(hdr.Gid), hdr.ModTime, rank
	}
}

// xattr is one setxattr of the header with the given rank. With del, it is
// the loss of the capability on chown.
type xattr struct {
	k, v string
	rank int64
	del  bool
}

// setXattr keeps the value with the highest rank for each name.
func setXattr(ino *Inode, x xattr) {
	for i := range ino.xs {
		if ino.xs[i].k == x.k {
			if x.rank > ino.xs[i].rank {
				ino.xs[i] = x
			}
			return
		}
	}
	ino.xs = append(ino.xs, x)
}

// addXattrs keeps the xattrs of a header. A hard link header without a
// capability removes it, because the lchown of docker removes it.
func addXattrs(ino *Inode, hdr *tar.Header, rank int64, link bool) {
	hasCap := false
	for k, v := range hdr.PAXRecords {
		if name, ok := strings.CutPrefix(k, "SCHILY.xattr."); ok {
			setXattr(ino, xattr{k: name, v: v, rank: rank})
			hasCap = hasCap || name == capXattr
		}
	}
	if link && !hasCap {
		setXattr(ino, xattr{k: capXattr, rank: rank, del: true})
	}
}

// applyMeta applies a header that is not a directory. The header with the
// highest rank sets owner and mtime, and a link header also sets the mode.
func applyMeta(ino *Inode, hdr *tar.Header, rank int64, link bool) {
	addXattrs(ino, hdr, rank, link)
	if rank <= ino.rank {
		return
	}
	ino.UID, ino.GID, ino.mtime, ino.rank = uint32(hdr.Uid), uint32(hdr.Gid), hdr.ModTime, rank
	if link && ino.mode.Type() != fs.ModeSymlink {
		ino.mode = ino.mode.Type() | hdr.FileInfo().Mode()&permBits
	}
}

func mergeMeta(dst, src *Inode) {
	for _, x := range src.xs {
		setXattr(dst, x)
	}
	if src.rank > dst.rank {
		dst.UID, dst.GID, dst.mtime, dst.rank = src.UID, src.GID, src.mtime, src.rank
		if dst.mode.Type() != fs.ModeSymlink {
			dst.mode = dst.mode.Type() | src.mode&permBits
		}
	}
}

// buildXattrs sets Inode.Xattrs after the merge.
func buildXattrs(ino *Inode) {
	for _, x := range ino.xs {
		if x.del {
			continue
		}
		if ino.Xattrs == nil {
			ino.Xattrs = make(map[string]string, len(ino.xs))
		}
		ino.Xattrs[x.k] = x.v
	}
	ino.xs = nil
}

// find follows the forward links of merged inodes.
func find(i *Inode) *Inode {
	for i.fwd != nil {
		if i.fwd.fwd != nil {
			i.fwd = i.fwd.fwd
		}
		i = i.fwd
	}
	return i
}

// forward makes a an alias of b.
func forward(a, b *Inode) {
	a, b = find(a), find(b)
	if a == b {
		return
	}
	mergeMeta(b, a)
	a.fwd = b
}

func (m *merger) create(tr *tar.Reader, parent *node, base string, hdr *tar.Header, l, i int32, replace bool) (*node, error) {
	rank := rankOf(l, i)
	name := strings.Clone(base)
	if isDirHdr(hdr) {
		d := m.newDir(parent, name, l)
		if replace {
			d.opaque, d.cut = rank, rank
		}
		m.applyDir(d, hdr, rank)
		return d, nil
	}
	n := &node{name: name, kind: kOther, layer: l, idx: i}
	switch {
	case isDevWhiteout(hdr):
		n.kind = kWhiteout
	case hdr.Typeflag == tar.TypeLink:
		ino, ok := m.linkInode(cleanName(hdr.Linkname), l)
		if !ok {
			return nil, nil
		}
		applyMeta(ino, hdr, rank, true)
		n.ino = ino
	default:
		n.ino = &Inode{rank: -1}
		if err := m.fill(n.ino, tr, hdr, rank); err != nil {
			return nil, err
		}
	}
	parent.add(n)
	return n, nil
}

// linkRef records the target of a hard link of layer l, as it is in layer l.
// The target is in this layer or in the next lower layer that has it.
func (m *merger) linkRef(target string, l int32) seenRec {
	rec := seenRec{isLink: true, target: target}
	if target == "" {
		rec.dead = true
		return rec
	}
	r := m.seen[target]
	if n := m.lookupMerge(target); n != nil && n.layer == l && (r == nil || n.idx > r.index) {
		if n.kind != kOther {
			rec.dead = true
		} else {
			rec.ino = n.ino
		}
		return rec
	}
	rec.via = r
	return rec
}

// recIno returns the inode of a recorded entry and makes it when necessary.
// It does not queue a second read, and it records a lower target as metaOnly.
func (m *merger) recIno(r *seenRec, l int32) *Inode {
	switch {
	case r.ino != nil || r.dead:
	case !r.isLink:
		r.ino = &Inode{rank: -1, state: stRefetch, refIdx: r.index}
	case r.via != nil:
		r.ino = m.recIno(r.via, l)
		r.dead = r.ino == nil
	default:
		r.ino = &Inode{rank: -1, state: stPending}
		r.pend = &pend{ino: r.ino, layer: l, metaOnly: true}
		m.pending[r.target] = append(m.pending[r.target], r.pend)
	}
	return r.ino
}

// activate marks the data of a recorded entry as necessary.
func (m *merger) activate(r *seenRec, l int32) {
	for ; r != nil; r = r.via {
		if r.pend != nil {
			r.pend.metaOnly = false
		}
		if !r.isLink && !r.queued && r.ino != nil && r.ino.state == stRefetch {
			r.queued = true
			m.refetch[l] = append(m.refetch[l], r.ino)
		}
	}
}

// linkInode returns the inode of a hard link of layer l.
func (m *merger) linkInode(target string, l int32) (*Inode, bool) {
	rec := m.linkRef(target, l)
	ino := m.recIno(&rec, l)
	m.activate(&rec, l)
	return ino, ino != nil
}

// lookupMerge finds the node at name in the tree that the merge makes.
func (m *merger) lookupMerge(name string) *node {
	n := m.root
	for name != "" {
		if n.kind != kDir {
			return nil
		}
		var comp string
		if j := strings.IndexByte(name, '/'); j >= 0 {
			comp, name = name[:j], name[j+1:]
		} else {
			comp, name = name, ""
		}
		n = n.kids[comp]
		if n == nil {
			return nil
		}
	}
	return n
}

// fill sets the type and the content of ino from a header that is not a link.
// It sets the mode bits unless a link header with a higher rank set them.
func (m *merger) fill(ino *Inode, tr *tar.Reader, hdr *tar.Header, rank int64) error {
	fi := hdr.FileInfo().Mode()
	typ := fi.Type()
	switch hdr.Typeflag {
	case tar.TypeSymlink:
		ino.target = hdr.Linkname
	case tar.TypeChar, tar.TypeBlock:
		ino.Major, ino.Minor = uint32(hdr.Devmajor), uint32(hdr.Devminor)
	case tar.TypeFifo:
	default:
		typ = 0
		ino.size = hdr.Size
		if err := m.store(ino, tr, hdr.Size); err != nil {
			return err
		}
	}
	if rank > ino.rank || typ == fs.ModeSymlink {
		ino.mode = typ | fi&permBits
	} else {
		ino.mode = typ | ino.mode&permBits
	}
	ino.state = stResolved
	applyMeta(ino, hdr, rank, false)
	return nil
}

func (m *merger) store(ino *Inode, r io.Reader, size int64) error {
	if m.opts.inMemory {
		ino.data = make([]byte, size)
		_, err := io.ReadFull(r, ino.data)
		return err
	}
	if size == 0 {
		return nil
	}
	if a := m.opts.align; a > 1 && m.spoolOff%a != 0 {
		pad := a - m.spoolOff%a
		if _, err := m.spoolW.Write(make([]byte, pad)); err != nil {
			return err
		}
		m.spoolOff += pad
	}
	ino.off = m.spoolOff
	n, err := io.CopyBuffer(m.spoolW, io.LimitReader(r, size), m.cpBuf)
	m.spoolOff += n
	if err == nil && n != size {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// pendingAbove reports if a link of a layer above l waits for name, and if
// one of these links needs the data.
func (m *merger) pendingAbove(name string, l int32) (need, ok bool) {
	for _, p := range m.pending[name] {
		if p.layer > l {
			ok = true
			need = need || !p.metaOnly
		}
	}
	return need, ok
}

// candidate returns the inode that entry i of layer l gives to the links
// that wait for its path. A later entry of the same layer replaces it.
func (m *merger) candidate(tr *tar.Reader, hdr *tar.Header, name string, l, i int32, vis *node, need bool) (*Inode, error) {
	switch {
	case isDirHdr(hdr), isDevWhiteout(hdr):
		return nil, nil
	case vis != nil:
		return vis.ino, nil
	}
	rec := m.seen[name]
	if hdr.Typeflag == tar.TypeLink {
		if rec == nil || rec.index != i {
			ino, _ := m.linkInode(cleanName(hdr.Linkname), l)
			return ino, nil
		}
		ino := m.recIno(rec, l)
		if need {
			m.activate(rec, l)
		}
		return ino, nil
	}
	// A hidden entry with content: read its data now if a link needs it.
	// Same-layer links after it use the inode of its record.
	if !need {
		return m.recIno(rec, l), nil
	}
	ino := &Inode{rank: -1}
	rec.ino, rec.queued = ino, true
	return ino, m.fill(ino, tr, hdr, rankOf(l, i))
}

// resolvePending gives the last entry of layer l at a path to the links that
// wait for that path. A nil candidate makes the links dangling.
func (m *merger) resolvePending(l int32) {
	for name, c := range m.cands {
		var canon *Inode
		ps := m.pending[name]
		keep := ps[:0]
		for _, p := range ps {
			if p.layer <= l {
				keep = append(keep, p)
			} else if canon == nil {
				canon = find(p.ino)
			} else {
				forward(p.ino, canon)
			}
		}
		if len(keep) == 0 {
			delete(m.pending, name)
		} else {
			m.pending[name] = keep
		}
		switch {
		case canon == nil:
		case c == nil:
			canon.state = stDangling
		default:
			forward(canon, c)
		}
	}
}

// refetchLayer reads layer l again for the data of hidden link targets.
func (m *merger) refetchLayer(l int32, inos []*Inode) error {
	slices.SortFunc(inos, func(a, b *Inode) int { return int(a.refIdx - b.refIdx) })
	tr, rc, err := m.open(l)
	if err != nil {
		return err
	}
	k := 0
	for i := int32(0); k < len(inos); i++ {
		hdr, err := tr.Next()
		if err != nil {
			_ = rc.Close()
			if err == io.EOF {
				err = errors.New("entry not found")
			}
			return err
		}
		if i != inos[k].refIdx {
			continue
		}
		if err := m.fill(find(inos[k]), tr, hdr, rankOf(l, i)); err != nil {
			_ = rc.Close()
			return err
		}
		k++
	}
	return m.finish(rc)
}

// finalize removes whiteouts and dangling links, sorts the directories and
// gives inode numbers in depth-first order.
func (m *merger) finalize() {
	var next uint64
	var walk func(d *node)
	walk = func(d *node) {
		next++
		d.ino.Ino = next
		d.list = make([]*node, 0, len(d.kids))
		subdirs := uint32(0)
		for _, c := range d.kids {
			switch c.kind {
			case kWhiteout:
				continue
			case kOther:
				c.ino = find(c.ino)
				if c.ino.state != stResolved {
					continue
				}
			case kDir:
				subdirs++
			}
			d.list = append(d.list, c)
		}
		d.kids = nil
		slices.SortFunc(d.list, func(a, b *node) int { return strings.Compare(a.name, b.name) })
		d.ino.Nlink = 2 + subdirs
		buildXattrs(d.ino)
		for _, c := range d.list {
			if c.kind == kDir {
				walk(c)
				continue
			}
			if c.ino.Ino == 0 {
				buildXattrs(c.ino)
				next++
				c.ino.Ino = next
			}
			c.ino.Nlink++
		}
	}
	walk(m.root)
}
