// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package mergefs

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/unikraft/go-archivefs"

	xio "unikraft.com/x/io"
)

const (
	whPrefix     = ".wh."                // prefix of a whiteout, which removes the path without the prefix
	whOpaque     = ".wh..wh..opq"        // opaque whiteout, which hides the lower content of its directory
	whMetaPrefix = ".wh..wh."            // prefix of the AUFS metadata at the root
	whLinkDir    = ".wh..wh.plnk"        // AUFS directory with the targets of hard links
	capXattr     = "security.capability" // xattr that a chown of a hard link removes
)

// kind is the type of a node during the merge.
type kind uint8

const (
	kDir      kind = iota // directory
	kOther                // file, symlink, device or fifo
	kWhiteout             // removed path, which hides the lower layers
)

// state tells if an inode has its type and its data.
type state uint8

const (
	stResolved state = iota // the inode has its type and its data
	stPending               // a hard link waits for its target in a lower layer
	stHidden                // an entry that an upper layer hides, with the data in the scratch file
	stDangling              // a hard link without a target
)

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
	filled bool     // the inode has the type and the data of the entry
	target string   // link target, for a lookup in a lower layer
	via    *seenRec // link to a different hidden entry
	ino    *Inode   // inode of the entry, made on first use
	pend   *pend
	hdr    *tar.Header // what fill needs of the header of an entry that is not a link
	off    int64       // offset of the data in the scratch file
	data   []byte      // data with InMemory
}

type merger struct {
	ctx         context.Context // the context of Merge, also for the holes of sparse files
	layers      []Layer
	opts        options
	root        *node
	spoolF      *os.File
	ownSpool    bool
	spoolLeft   bool // the spool is still in its directory
	spoolW      *bufio.Writer
	spoolOff    int64
	scratch     *os.File // data of the hidden files of the current layer
	scratchLeft bool     // the scratch file is still in its directory
	kept        int64    // bytes of hidden data of the current layer, in the scratch file or in memory
	cpBuf       []byte
	pending     map[string][]*pend
	under       map[string]int                 // the number of pending targets below each directory
	pendingIn   map[string]map[string]struct{} // the pending targets directly in each directory
	pendingDirs map[string]map[string]struct{} // the subdirectories of each directory that have pending targets below
	seen        map[string]*seenRec
	cands       map[string]*Inode
	above       map[string]aboveLinks // the answers of pendingAbove in the current layer
	stored      int64                 // bytes of file data in the spool or in memory, for WithMaxSize
	moved       int64                 // bytes of hidden data that a link copied to the spool or to memory, counted in stored
	entries     int64                 // tar entries of the layers and the directories that their names imply, for WithMaxEntries
	meta        int64                 // bytes of the xattrs and the link targets of all the entries, for WithMaxSize
	read        int64                 // bytes read from the layers, for WithMaxReadSize
}

func newMerger(layers []Layer, o options) (*merger, error) {
	m := &merger{
		layers:  layers,
		opts:    o,
		pending: map[string][]*pend{},
		under:   map[string]int{},
		cpBuf:   make([]byte, 1<<20),

		pendingIn:   map[string]map[string]struct{}{},
		pendingDirs: map[string]map[string]struct{}{},
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
		f, left, err := tempFile(o.spoolDir, "mergefs-spool-*")
		if err != nil {
			return nil, err
		}
		m.spoolF, m.ownSpool, m.spoolLeft = f, true, left
	}
	m.spoolW = bufio.NewWriterSize(onlyWriter{io.NewOffsetWriter(m.spoolF, 0)}, 1<<20)
	return m, nil
}

// tempFile makes a temporary file and removes it from its directory, which Windows cannot do while it is open.
func tempFile(dir, pattern string) (f *os.File, left bool, err error) {
	f, err = os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, false, err
	}
	return f, os.Remove(f.Name()) != nil, nil
}

func (m *merger) close() {
	if m.spoolF != nil && m.ownSpool {
		_ = closeSpool(m.spoolF, m.spoolLeft)
	}
	m.closeScratch()
}

func (m *merger) closeScratch() {
	if m.scratch != nil {
		_ = closeSpool(m.scratch, m.scratchLeft)
		m.scratch = nil
	}
}

// closeSpool closes a temporary file of Merge, and removes it when tempFile could not.
func closeSpool(f *os.File, left bool) error {
	err := f.Close()
	if left {
		err = errors.Join(err, os.Remove(f.Name()))
	}
	return err
}

func (m *merger) run(ctx context.Context) error {
	m.ctx = ctx
	for l := range slices.Backward(m.layers) {
		if err := m.readLayer(ctx, int32(l)); err != nil {
			return fmt.Errorf("layer %d: %w", l, err)
		}
	}
	m.seen, m.above = nil, nil
	m.closeScratch()
	for _, ps := range m.pending {
		for _, p := range ps {
			find(p.ino).state = stDangling
		}
	}
	m.pending, m.under, m.pendingIn, m.pendingDirs = nil, nil, nil, nil
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

func (m *merger) open(ctx context.Context, l int32) (*tar.Reader, io.ReadCloser, error) {
	rc, err := m.layers[l]()
	if err != nil {
		return nil, nil, err
	}
	return tar.NewReader(readCounter{xio.ContextReader(ctx, rc), m}), rc, nil
}

// readCounter counts the bytes that Merge reads from the layers, for WithMaxReadSize.
type readCounter struct {
	r io.Reader
	m *merger
}

func (c readCounter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.m.read += int64(n)
	if limit := c.m.opts.maxRead; limit > 0 && c.m.read > limit {
		return n, fmt.Errorf("%w: more than %d bytes", ErrLayersTooLarge, limit)
	}
	return n, err
}

// finish reads the stream to the end, so that a verifying reader sees EOF.
func (m *merger) finish(ctx context.Context, rc io.ReadCloser) error {
	_, err := io.CopyBuffer(io.Discard, readCounter{xio.ContextReader(ctx, rc), m}, m.cpBuf)
	if cerr := rc.Close(); err == nil {
		err = cerr
	}
	return err
}

func (m *merger) readLayer(ctx context.Context, l int32) error {
	tr, rc, err := m.open(ctx, l)
	if err != nil {
		return err
	}
	m.seen = map[string]*seenRec{}
	m.cands = map[string]*Inode{}
	m.above = map[string]aboveLinks{}
	for i := int32(0); ; i++ {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = rc.Close()
			return err
		}
		m.entries++
		if err := m.checkEntries(); err != nil {
			_ = rc.Close()
			return err
		}
		m.meta += metaBytes(hdr)
		if err := m.checkSize(0); err != nil {
			_ = rc.Close()
			return err
		}
		if err := m.entry(tr, hdr, l, i); err != nil {
			_ = rc.Close()
			return fmt.Errorf("%s: %w", hdr.Name, err)
		}
		// The directories that the name implies count too.
		if err := m.checkEntries(); err != nil {
			_ = rc.Close()
			return err
		}
	}
	m.resolvePending(l)
	if err := m.clearScratch(); err != nil {
		_ = rc.Close()
		return err
	}
	return m.finish(ctx, rc)
}

// Linux refuses longer names: NAME_MAX bytes for one component and PATH_MAX bytes for a path.
const (
	maxComponent = 255
	maxName      = 4096
)

// ErrNameTooLong is the error of Merge for an entry name or link target that Linux refuses: a component over 255 bytes, or more than 4096 bytes.
var ErrNameTooLong = errors.New("rootfs has a name that is too long")

// checkName returns ErrNameTooLong for a name that Linux refuses. A whiteout counts without its prefix.
func checkName(name string) error {
	if len(name) > maxName {
		return fmt.Errorf("%w: %d bytes", ErrNameTooLong, len(name))
	}
	for comp := range strings.SplitSeq(name, "/") {
		n := len(comp)
		if strings.HasPrefix(comp, whPrefix) {
			n -= len(whPrefix)
		}
		if n > maxComponent {
			return fmt.Errorf("%w: a component of %d bytes", ErrNameTooLong, n)
		}
	}
	return nil
}

// Linux refuses larger xattrs: XATTR_NAME_MAX, XATTR_SIZE_MAX, and XATTR_LIST_MAX for the names of one inode.
const (
	maxXattrName  = 255
	maxXattrValue = 65536
	maxXattrList  = 65536
)

// ErrXattrTooLarge is the error of Merge for an xattr that Linux refuses: a name over 255 bytes, a value over 64 KiB, or names of one entry over 64 KiB in all.
var ErrXattrTooLarge = errors.New("rootfs has an xattr that is too large")

// checkXattrs returns ErrXattrTooLarge for xattrs of a header that Linux refuses.
func checkXattrs(hdr *tar.Header) error {
	xattrs, _ := archivefs.GetXattrs(hdr)
	list := 0
	for name, v := range xattrs {
		if len(name) > maxXattrName || len(v) > maxXattrValue {
			return fmt.Errorf("%w: %d bytes of name and %d bytes of value", ErrXattrTooLarge, len(name), len(v))
		}
		if list += len(name) + 1; list > maxXattrList {
			return fmt.Errorf("%w: names of more than %d bytes", ErrXattrTooLarge, maxXattrList)
		}
	}
	return nil
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

func rankOf(l, i int32) int64 { return int64(l)<<32 | int64(uint32(i)) }

func isDirHdr(h *tar.Header) bool { return h.Typeflag == tar.TypeDir }

// keptHeader returns the fields of hdr that fill needs, for a hidden entry that a later hard link of the layer can use. It drops the other PAX records, which can be large.
func keptHeader(hdr *tar.Header) *tar.Header {
	h := &tar.Header{
		Typeflag: hdr.Typeflag, Mode: hdr.Mode, Uid: hdr.Uid, Gid: hdr.Gid, Size: hdr.Size, ModTime: hdr.ModTime,
		Linkname: hdr.Linkname, Devmajor: hdr.Devmajor, Devminor: hdr.Devminor,
	}
	for k, v := range hdr.PAXRecords {
		if strings.HasPrefix(k, "SCHILY.xattr.") {
			if h.PAXRecords == nil {
				h.PAXRecords = map[string]string{}
			}
			h.PAXRecords[k] = v
		}
	}
	return h
}

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
	if err := checkName(name); err != nil {
		return err
	}
	if len(hdr.Linkname) > maxName {
		return fmt.Errorf("%w: a link target of %d bytes", ErrNameTooLong, len(hdr.Linkname))
	}
	if err := checkXattrs(hdr); err != nil {
		return err
	}
	rank := rankOf(l, i)
	if name == "" {
		if isDirHdr(hdr) {
			return m.applyDir(m.root, hdr, rank)
		}
		return nil
	}
	// Like docker, keep only the files of .wh..wh.plnk from the AUFS metadata at the root, for hard links.
	aufs := strings.HasPrefix(name, whMetaPrefix) && name != whOpaque
	if aufs && name != whLinkDir && !strings.HasPrefix(name, whLinkDir+"/") {
		return nil
	}
	dir, base := splitName(name)
	if strings.HasPrefix(base, whPrefix) && !aufs {
		if base == whOpaque {
			m.danglePending(dir, l, false)
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
				if err := m.applyDir(ex, hdr, rank); err != nil {
					return err
				}
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
				if err := applyMeta(ino, hdr, rank, true); err != nil {
					return err
				}
			}
		}
		if isDevWhiteout(hdr) {
			rec.dead = true
		} else if hdr.Typeflag != tar.TypeLink {
			rec.hdr = keptHeader(hdr)
		}
		m.seen[name] = rec
	}

	if len(m.pending) > 0 {
		if !isDirHdr(hdr) {
			m.danglePending(name, l, true)
		}
		if need, ok := m.pendingAbove(name, l); ok {
			c, err := m.candidate(tr, hdr, name, l, i, vis, need)
			if err != nil {
				return err
			}
			m.cands[name] = c
		}
	}
	if rec := m.seen[name]; !visible && rec != nil && rec.index == i && rec.hdr != nil && !rec.filled {
		return m.keep(rec, tr)
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
		comp, dir, _ = strings.Cut(dir, "/")
		c := n.kids[comp]
		switch {
		case c == nil:
			c = m.newDir(n, strings.Clone(comp), l)
			m.entries++
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

// metaBytes returns the bytes of a header that the merge can keep: the names and the values of its xattrs, and its link target.
func metaBytes(hdr *tar.Header) int64 {
	n := int64(len(hdr.Linkname))
	for k, v := range hdr.PAXRecords {
		if name, ok := strings.CutPrefix(k, "SCHILY.xattr."); ok {
			n += int64(len(name) + len(v))
		}
	}
	return n
}

// checkEntries returns ErrTooManyEntries when the entries are over the limit of WithMaxEntries.
func (m *merger) checkEntries() error {
	if limit := m.opts.maxEntries; limit > 0 && m.entries > limit {
		return fmt.Errorf("%w: more than %d", ErrTooManyEntries, limit)
	}
	return nil
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
	n.mark(c.layer)
	n.marked = append(n.marked, c)
}

// purge removes from the subtree of d the entries and the xattrs that layer l
// added. It visits only the nodes that mark recorded for layer l.
func (m *merger) purge(d *node, l int32) {
	if d.last != l+1 {
		return
	}
	marked := d.marked
	d.last, d.marked = 0, nil
	if d.ino.rank >= 0 && int32(d.ino.rank>>32) == l {
		*d.ino = Inode{mode: fs.ModeDir | 0o755, rank: -1}
	}
	for k, x := range d.ino.xs {
		if int32(x.rank>>32) == l {
			delete(d.ino.xs, k)
			d.ino.xlist -= len(k) + 1
		}
	}
	for _, c := range marked {
		if d.kids[c.name] != c {
			continue
		}
		switch {
		case c.layer == l:
			delete(d.kids, c.name)
		case c.kind == kDir:
			m.purge(c, l)
		}
	}
}

// mark records for purge that layer l changed n: it marks n and its parents,
// and lists each newly marked node in its parent.
func (n *node) mark(l int32) {
	if n.last == l+1 {
		return
	}
	if n.parent != nil {
		n.parent.mark(l)
	}
	n.last, n.marked = l+1, n.marked[:0]
	if n.parent != nil {
		n.parent.marked = append(n.parent.marked, n)
	}
}

func (m *merger) whiteout(dir, name string, l, i int32) {
	if name == "" {
		return
	}
	target := name
	if dir != "" {
		target = dir + "/" + name
	}
	if len(m.pending) > 0 {
		if _, ok := m.pendingAbove(target, l); ok {
			if _, ok := m.cands[target]; !ok {
				m.cands[target] = nil
			}
		}
		m.danglePending(target, l, false)
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

func (m *merger) applyDir(n *node, hdr *tar.Header, rank int64) error {
	if rank < n.cut {
		return nil
	}
	n.mark(int32(rank >> 32))
	if rank > n.ino.rank {
		n.ino.mode = fs.ModeDir | hdr.FileInfo().Mode()&permBits
	}
	if err := addXattrs(n.ino, hdr, rank, false); err != nil {
		return err
	}
	if rank > n.ino.rank {
		n.ino.UID, n.ino.GID, n.ino.mtime, n.ino.rank = uint32(hdr.Uid), uint32(hdr.Gid), hdr.ModTime, rank
	}
	return nil
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
	if old, ok := ino.xs[x.k]; ok {
		if x.rank > old.rank {
			ino.xs[x.k] = x
		}
		return
	}
	if ino.xs == nil {
		ino.xs = map[string]xattr{}
	}
	ino.xs[x.k] = x
	ino.xlist += len(x.k) + 1
}

// addXattrs keeps the xattrs of a header. A hard link header without a
// capability removes it, because the lchown of docker removes it.
func addXattrs(ino *Inode, hdr *tar.Header, rank int64, link bool) error {
	xattrs, _ := archivefs.GetXattrs(hdr)
	for name, v := range xattrs {
		setXattr(ino, xattr{k: name, v: v, rank: rank})
	}
	if _, hasCap := xattrs[capXattr]; link && !hasCap {
		setXattr(ino, xattr{k: capXattr, rank: rank, del: true})
	}
	if ino.xlist > maxXattrList {
		return fmt.Errorf("%w: names of more than %d bytes", ErrXattrTooLarge, maxXattrList)
	}
	return nil
}

// applyMeta applies a header that is not a directory. The header with the
// highest rank sets owner and mtime, and a link header also sets the mode.
func applyMeta(ino *Inode, hdr *tar.Header, rank int64, link bool) error {
	if err := addXattrs(ino, hdr, rank, link); err != nil {
		return err
	}
	if rank <= ino.rank {
		return nil
	}
	ino.UID, ino.GID, ino.mtime, ino.rank = uint32(hdr.Uid), uint32(hdr.Gid), hdr.ModTime, rank
	if link && ino.mode.Type() != fs.ModeSymlink {
		ino.mode = ino.mode.Type() | hdr.FileInfo().Mode()&permBits
	}
	return nil
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
		return d, m.applyDir(d, hdr, rank)
	}
	n := &node{name: name, kind: kOther, layer: l, idx: i}
	switch {
	case isDevWhiteout(hdr):
		n.kind = kWhiteout
	case hdr.Typeflag == tar.TypeLink:
		ino, err := m.linkInode(cleanName(hdr.Linkname), l)
		if err != nil {
			return nil, err
		}
		if ino == nil {
			// Like a dangling link, a link without a valid target still hides the lower layers.
			n.kind = kWhiteout
			break
		}
		if err := applyMeta(ino, hdr, rank, true); err != nil {
			return nil, err
		}
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
// It does not read the data, and it records a lower target as metaOnly.
func (m *merger) recIno(r *seenRec, l int32) *Inode {
	switch {
	case r.ino != nil || r.dead:
	case !r.isLink:
		r.ino = &Inode{rank: -1, state: stHidden}
	case r.via != nil:
		r.ino = m.recIno(r.via, l)
		r.dead = r.ino == nil
	default:
		r.ino = &Inode{rank: -1, state: stPending}
		r.pend = &pend{ino: r.ino, layer: l, metaOnly: true}
		if len(m.pending[r.target]) == 0 {
			m.countUnder(r.target, 1)
		}
		m.pending[r.target] = append(m.pending[r.target], r.pend)
	}
	return r.ino
}

// activate marks the data of a recorded entry as necessary, and fills the inode of a hidden entry of the current layer.
func (m *merger) activate(r *seenRec, l int32) error {
	for ; r != nil; r = r.via {
		if r.pend != nil {
			r.pend.metaOnly = false
		}
		if !r.isLink && !r.filled && r.ino != nil && r.ino.state == stHidden {
			r.filled = true
			if err := m.fillHidden(r, l); err != nil {
				return err
			}
		}
	}
	return nil
}

// linkInode returns the inode of a hard link of layer l, or nil when the link has no valid target.
func (m *merger) linkInode(target string, l int32) (*Inode, error) {
	rec := m.linkRef(target, l)
	ino := m.recIno(&rec, l)
	return ino, m.activate(&rec, l)
}

// lookupMerge finds the node at name in the tree that the merge makes.
func (m *merger) lookupMerge(name string) *node {
	n := m.root
	for name != "" {
		if n.kind != kDir {
			return nil
		}
		var comp string
		comp, name, _ = strings.Cut(name, "/")
		n = n.kids[comp]
		if n == nil {
			return nil
		}
	}
	return n
}

// fill sets the type and the content of ino from a header that is not a link,
// and reads the data from r. It sets the mode bits unless a link header with a
// higher rank set them.
func (m *merger) fill(ino *Inode, r io.Reader, hdr *tar.Header, rank int64) error {
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
		if err := m.store(ino, r, hdr.Size); err != nil {
			return err
		}
	}
	if rank > ino.rank || typ == fs.ModeSymlink {
		ino.mode = typ | fi&permBits
	} else {
		ino.mode = typ | ino.mode&permBits
	}
	ino.state = stResolved
	if err := applyMeta(ino, hdr, rank, false); err != nil {
		return err
	}
	return nil
}

// checkSize fails when size more bytes of data go over WithMaxSize.
func (m *merger) checkSize(size int64) error {
	if limit := m.opts.maxSize; limit > 0 && size > limit-m.stored-m.meta-(m.kept-m.moved) {
		return fmt.Errorf("%w: more than %d bytes", ErrTooLarge, limit)
	}
	return nil
}

func (m *merger) store(ino *Inode, r io.Reader, size int64) error {
	if err := m.checkSize(size); err != nil {
		return err
	}
	m.stored += size
	// tar.Reader makes the holes of a sparse file without a read of the layer, so check the context here too.
	r = xio.ContextReader(m.ctx, r)
	if m.opts.inMemory {
		// ReadAll grows the buffer with the data that it reads, not with the size in the header.
		data, err := io.ReadAll(io.LimitReader(r, size))
		if err == nil && int64(len(data)) != size {
			err = io.ErrUnexpectedEOF
		}
		ino.data = data
		return err
	}
	if size == 0 {
		return nil
	}
	ino.off = m.spoolOff
	n, err := io.CopyBuffer(m.spoolW, io.LimitReader(r, size), m.cpBuf)
	m.spoolOff += n
	if err == nil && n != size {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// hasData reports if a header that is not a link has file data.
func hasData(hdr *tar.Header) bool {
	switch hdr.Typeflag {
	case tar.TypeSymlink, tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
		return false
	}
	return hdr.Size > 0
}

// keep writes the data of a hidden entry of the current layer to the scratch file, for a later hard link of the layer.
func (m *merger) keep(r *seenRec, tr *tar.Reader) error {
	if !hasData(r.hdr) {
		return nil
	}
	if err := m.checkSize(r.hdr.Size); err != nil {
		return err
	}
	src := io.LimitReader(xio.ContextReader(m.ctx, tr), r.hdr.Size)
	if m.opts.inMemory {
		data, err := io.ReadAll(src)
		if err == nil && int64(len(data)) != r.hdr.Size {
			err = io.ErrUnexpectedEOF
		}
		r.data = data
		m.kept += int64(len(data))
		return err
	}
	if m.scratch == nil {
		f, left, err := tempFile(m.opts.spoolDir, "mergefs-scratch-*")
		if err != nil {
			return err
		}
		m.scratch, m.scratchLeft = f, left
	}
	r.off = m.kept
	n, err := io.CopyBuffer(onlyWriter{io.NewOffsetWriter(m.scratch, m.kept)}, src, m.cpBuf)
	m.kept += n
	if err == nil && n != r.hdr.Size {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// fillHidden fills the inode of a hidden entry of the current layer from its header and the data that keep wrote.
func (m *merger) fillHidden(r *seenRec, l int32) error {
	var data io.Reader = bytes.NewReader(r.data)
	if m.scratch != nil && hasData(r.hdr) {
		data = io.NewSectionReader(m.scratch, r.off, r.hdr.Size)
	}
	if hasData(r.hdr) {
		m.moved += r.hdr.Size
	}
	return m.fill(find(r.ino), data, r.hdr, rankOf(l, r.index))
}

// clearScratch drops the hidden data at the end of a layer.
func (m *merger) clearScratch() error {
	n := m.kept
	m.kept, m.moved = 0, 0
	if m.scratch == nil || n == 0 {
		return nil
	}
	return m.scratch.Truncate(0)
}

// pendingAbove reports if a link of a layer above l waits for name, and if
// one of these links needs the data. The links of the layers above l do not
// change while the merge reads layer l, so it keeps the answer for the layer.
func (m *merger) pendingAbove(name string, l int32) (need, ok bool) {
	if a, hit := m.above[name]; hit {
		return a.need, a.ok
	}
	for _, p := range m.pending[name] {
		if p.layer > l {
			ok = true
			need = need || !p.metaOnly
		}
	}
	m.above[name] = aboveLinks{need: need, ok: ok}
	return need, ok
}

// aboveLinks is the answer of pendingAbove for one name in the current layer.
type aboveLinks struct{ need, ok bool }

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
			return m.linkInode(cleanName(hdr.Linkname), l)
		}
		ino := m.recIno(rec, l)
		if need {
			return ino, m.activate(rec, l)
		}
		return ino, nil
	}
	// A hidden entry with content: read its data now if a link needs it.
	// Same-layer links after it use the inode of its record.
	if !need {
		return m.recIno(rec, l), nil
	}
	ino := &Inode{rank: -1}
	rec.ino, rec.filled = ino, true
	return ino, m.fill(ino, tr, hdr, rankOf(l, i))
}

// countUnder adds d to the count of pending targets below each parent directory of name, and keeps the indexes of danglePending.
func (m *merger) countUnder(name string, d int) {
	dir, _ := splitName(name)
	indexSet(m.pendingIn, dir, name, d > 0)
	for name != "" {
		name, _ = splitName(name)
		had := m.under[name] > 0
		if m.under[name] += d; m.under[name] == 0 {
			delete(m.under, name)
		}
		if has := m.under[name] > 0; name != "" && has != had {
			parent, _ := splitName(name)
			indexSet(m.pendingDirs, parent, name, has)
		}
	}
}

// indexSet adds key to the set of dir in idx, or removes it.
func indexSet(idx map[string]map[string]struct{}, dir, key string, add bool) {
	if add {
		if idx[dir] == nil {
			idx[dir] = map[string]struct{}{}
		}
		idx[dir][key] = struct{}{}
		return
	}
	delete(idx[dir], key)
	if len(idx[dir]) == 0 {
		delete(idx, dir)
	}
}

// danglePending makes dangling the upper links that wait for a path below dir, also for entries of layer l with replace.
func (m *merger) danglePending(dir string, l int32, replace bool) {
	if m.under[dir] == 0 {
		return
	}
	// Walk only the directories below dir that have pending targets.
	dirs := []string{dir}
	for len(dirs) > 0 {
		d := dirs[len(dirs)-1]
		dirs = dirs[:len(dirs)-1]
		for name := range m.pendingIn[d] {
			if _, ok := m.pendingAbove(name, l); !ok {
				continue
			}
			if _, ok := m.cands[name]; !ok || replace {
				m.cands[name] = nil
			}
		}
		for sub := range m.pendingDirs[d] {
			dirs = append(dirs, sub)
		}
	}
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
			m.countUnder(name, -1)
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
			// Like docker, leave out the AUFS metadata at the root, such as .wh..wh.plnk.
			if d == m.root && strings.HasPrefix(c.name, whMetaPrefix) {
				continue
			}
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
