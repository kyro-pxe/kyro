// Copyright (C) 2026 Lukas Koch and Kyro contributors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This file is part of Kyro.
//
// Kyro is free software: you can redistribute it and/or modify it under the
// terms of the GNU General Public License as published by the Free Software
// Foundation, either version 3 of the License, or (at your option) any later
// version.
//
// Kyro is distributed in the hope that it will be useful, but WITHOUT ANY
// WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS
// FOR A PARTICULAR PURPOSE. See the GNU General Public License for more
// details.
//
// You should have received a copy of the GNU General Public License along
// with Kyro. If not, see <https://www.gnu.org/licenses/>.

// Package udf reads files out of a UDF image without mounting it.
//
// Windows installation media are UDF. Kyro serves boot.wim over HTTP and
// the whole tree over SMB straight from the ISO, so nothing has to be
// unpacked and no root rights are needed to mount anything.
//
// The package implements the small part of ECMA-167 and UDF that such media
// use: a sector size of 2048 bytes, one physical partition, file entries
// with strategy 4, and short, long or embedded allocation descriptors. That
// covers UDF 1.02 to 2.01. Media with a metadata partition (UDF 2.50 and
// later) are rejected with ErrUnsupported.
//
// An image is treated as untrusted input: every offset and length that
// comes from it is checked against the size of the image, and every loop
// over its structures is bounded.
package udf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

const (
	sectorSize = 2048

	// Descriptor tag identifiers (ECMA-167 3/7.2.1 and 4/7.2.1).
	tagAnchor          = 2
	tagPartition       = 5
	tagLogicalVolume   = 6
	tagTerminating     = 8
	tagFileSet         = 256
	tagFileIdentifier  = 257
	tagAllocExtent     = 258
	tagFileEntry       = 261
	tagExtendedFileEnt = 266

	anchorSector = 256

	// Bounds on what an image may make the reader do.
	maxVDSSectors   = 512
	maxPartitionMap = 16
	maxDirBytes     = 16 << 20
	maxExtents      = 1 << 16
	maxADHops       = 1 << 10
	maxCachedDirs   = 4096
)

var (
	// ErrNotUDF means that the image has no UDF anchor where one must be.
	ErrNotUDF = errors.New("udf: not a UDF image")
	// ErrUnsupported means that the image is UDF but uses a feature this
	// package does not implement.
	ErrUnsupported = errors.New("udf: unsupported UDF feature")
	// ErrCorrupt means that a structure in the image is inconsistent.
	ErrCorrupt = errors.New("udf: corrupt image")
)

func corrupt(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(format, a...))
}

// partition is one physical partition of the image.
type partition struct {
	start  uint32 // first sector
	blocks uint32
}

// longAD is a block address with the partition it is in.
type longAD struct {
	length uint32 // top two bits are the extent type
	block  uint32
	part   uint16
}

func parseLongAD(b []byte) longAD {
	return longAD{
		length: binary.LittleEndian.Uint32(b[0:]),
		block:  binary.LittleEndian.Uint32(b[4:]),
		part:   binary.LittleEndian.Uint16(b[8:]),
	}
}

// extent is a run of file data.
type extent struct {
	fileOff int64 // where the run starts in the file
	imgOff  int64 // where it starts in the image, -1 for a hole
	length  int64
}

// node is a file or directory.
type node struct {
	name    string
	dir     bool
	size    int64
	modTime time.Time
	extents []extent
	inline  []byte // content of a file that is stored inside its entry
}

// FS is a UDF image. It implements fs.FS, fs.ReadDirFS and fs.StatFS, and
// its files implement io.ReaderAt and io.Seeker.
type FS struct {
	r     io.ReaderAt
	size  int64
	parts []partition // indexed by partition reference
	root  *node

	mu   sync.Mutex
	dirs map[*node][]*dirent
}

// dirent is one entry of a directory. Its node is read on first use.
type dirent struct {
	name string
	dir  bool
	icb  longAD

	once sync.Once
	node *node
	err  error
}

// Open reads the volume structures of an image of the given size.
func Open(r io.ReaderAt, size int64) (*FS, error) {
	f := &FS{r: r, size: size, dirs: make(map[*node][]*dirent)}

	anchor, err := f.sector(anchorSector)
	if err != nil || checkTag(anchor, tagAnchor) != nil {
		return nil, ErrNotUDF
	}
	vdsLen := binary.LittleEndian.Uint32(anchor[16:])
	vdsLoc := binary.LittleEndian.Uint32(anchor[20:])
	sectors := vdsLen / sectorSize
	if sectors == 0 || sectors > maxVDSSectors {
		return nil, corrupt("volume descriptor sequence of %d bytes", vdsLen)
	}

	type partDesc struct {
		number uint16
		partition
	}
	var (
		partDescs []partDesc
		lvd       []byte
		lvdSeq    uint32
	)
	for i := uint32(0); i < sectors; i++ {
		d, err := f.sector(int64(vdsLoc) + int64(i))
		if err != nil {
			return nil, err
		}
		id := binary.LittleEndian.Uint16(d)
		if id == 0 || id == tagTerminating {
			break
		}
		if checkTagSum(d) != nil {
			return nil, corrupt("volume descriptor in sector %d has a bad tag", vdsLoc+i)
		}
		switch id {
		case tagPartition:
			partDescs = append(partDescs, partDesc{
				number: binary.LittleEndian.Uint16(d[22:]),
				partition: partition{
					start:  binary.LittleEndian.Uint32(d[188:]),
					blocks: binary.LittleEndian.Uint32(d[192:]),
				},
			})
		case tagLogicalVolume:
			// Of several, the one with the highest sequence number counts.
			if seq := binary.LittleEndian.Uint32(d[16:]); lvd == nil || seq >= lvdSeq {
				lvd, lvdSeq = d, seq
			}
		}
	}
	if lvd == nil || len(partDescs) == 0 {
		return nil, corrupt("no logical volume or no partition")
	}
	if bs := binary.LittleEndian.Uint32(lvd[212:]); bs != sectorSize {
		return nil, fmt.Errorf("%w: logical block size %d", ErrUnsupported, bs)
	}

	// The partition maps translate a partition reference into a partition.
	mapLen := binary.LittleEndian.Uint32(lvd[264:])
	mapCount := binary.LittleEndian.Uint32(lvd[268:])
	if mapCount == 0 || mapCount > maxPartitionMap || mapLen > sectorSize-440 {
		return nil, corrupt("%d partition maps in %d bytes", mapCount, mapLen)
	}
	maps := lvd[440 : 440+mapLen]
	for i := uint32(0); i < mapCount; i++ {
		if len(maps) < 2 || int(maps[1]) < 2 || int(maps[1]) > len(maps) {
			return nil, corrupt("partition map %d is cut off", i)
		}
		if maps[0] != 1 || maps[1] != 6 {
			return nil, fmt.Errorf("%w: partition map of type %d (UDF 2.50 or a sparing table)", ErrUnsupported, maps[0])
		}
		number := binary.LittleEndian.Uint16(maps[4:])
		found := false
		for _, pd := range partDescs {
			if pd.number == number {
				if (int64(pd.start)+int64(pd.blocks))*sectorSize > size {
					return nil, corrupt("partition %d ends after the image", number)
				}
				f.parts = append(f.parts, pd.partition)
				found = true
				break
			}
		}
		if !found {
			return nil, corrupt("partition map %d names partition %d, which does not exist", i, number)
		}
		maps = maps[maps[1]:]
	}

	fsd, err := f.block(parseLongAD(lvd[248:]))
	if err != nil {
		return nil, err
	}
	if err := checkTag(fsd, tagFileSet); err != nil {
		return nil, corrupt("file set descriptor: %v", err)
	}
	root, err := f.readNode(parseLongAD(fsd[400:]), "")
	if err != nil {
		return nil, fmt.Errorf("root directory: %w", err)
	}
	if !root.dir {
		return nil, corrupt("the root is not a directory")
	}
	f.root = root
	return f, nil
}

// checkTagSum verifies the checksum of a descriptor tag.
func checkTagSum(b []byte) error {
	var sum byte
	for i := 0; i < 16; i++ {
		if i != 4 {
			sum += b[i]
		}
	}
	if sum != b[4] {
		return errors.New("bad tag checksum")
	}
	return nil
}

// checkTag verifies that b starts with a valid descriptor tag of one kind.
func checkTag(b []byte, want uint16) error {
	if id := binary.LittleEndian.Uint16(b); id != want {
		return fmt.Errorf("tag %d where %d was expected", id, want)
	}
	return checkTagSum(b)
}

// sector reads one sector of the image.
func (f *FS) sector(n int64) ([]byte, error) {
	off := n * sectorSize
	if n < 0 || off+sectorSize > f.size {
		return nil, corrupt("sector %d is outside the image", n)
	}
	b := make([]byte, sectorSize)
	if _, err := f.r.ReadAt(b, off); err != nil {
		return nil, fmt.Errorf("udf: reading sector %d: %w", n, err)
	}
	return b, nil
}

// offset translates a block address into a byte offset in the image.
func (f *FS) offset(part uint16, block uint32) (int64, error) {
	if int(part) >= len(f.parts) {
		return 0, corrupt("reference to partition %d", part)
	}
	p := f.parts[part]
	if block >= p.blocks {
		return 0, corrupt("block %d is outside its partition", block)
	}
	return (int64(p.start) + int64(block)) * sectorSize, nil
}

// block reads the block that an address points to.
func (f *FS) block(ad longAD) ([]byte, error) {
	off, err := f.offset(ad.part, ad.block)
	if err != nil {
		return nil, err
	}
	return f.sector(off / sectorSize)
}

// readNode reads the file entry at an address.
func (f *FS) readNode(icb longAD, name string) (*node, error) {
	b, err := f.block(icb)
	if err != nil {
		return nil, err
	}
	var eaLen, adLen uint32
	var adStart int
	var mtime []byte
	switch binary.LittleEndian.Uint16(b) {
	case tagFileEntry:
		eaLen = binary.LittleEndian.Uint32(b[168:])
		adLen = binary.LittleEndian.Uint32(b[172:])
		adStart, mtime = 176, b[84:96]
	case tagExtendedFileEnt:
		eaLen = binary.LittleEndian.Uint32(b[208:])
		adLen = binary.LittleEndian.Uint32(b[212:])
		adStart, mtime = 216, b[92:104]
	default:
		return nil, corrupt("no file entry where %q should have one", name)
	}
	if err := checkTagSum(b); err != nil {
		return nil, corrupt("file entry of %q: %v", name, err)
	}
	if strategy := binary.LittleEndian.Uint16(b[20:]); strategy != 4 {
		return nil, fmt.Errorf("%w: ICB strategy %d", ErrUnsupported, strategy)
	}
	fileType := b[27]
	flags := binary.LittleEndian.Uint16(b[34:])
	infoLen := binary.LittleEndian.Uint64(b[56:])
	if infoLen > uint64(f.size) {
		return nil, corrupt("%q claims %d bytes, more than the image holds", name, infoLen)
	}
	if uint64(adStart)+uint64(eaLen)+uint64(adLen) > sectorSize {
		return nil, corrupt("file entry of %q does not fit into its block", name)
	}
	ads := b[adStart+int(eaLen) : adStart+int(eaLen)+int(adLen)]

	n := &node{name: name, size: int64(infoLen), modTime: parseTime(mtime)}
	switch fileType {
	case 4:
		n.dir = true
	case 5:
	default:
		// Symbolic links, devices and the like. They have no place on
		// installation media and are not served.
		return nil, fmt.Errorf("%w: file type %d", ErrUnsupported, fileType)
	}

	switch adType := flags & 7; adType {
	case 3:
		if uint64(len(ads)) < infoLen {
			return nil, corrupt("embedded data of %q is cut off", name)
		}
		n.inline = ads[:infoLen]
	case 0, 1:
		if n.extents, err = f.readExtents(ads, adType == 1, icb.part, n.size); err != nil {
			return nil, fmt.Errorf("%q: %w", name, err)
		}
	default:
		return nil, fmt.Errorf("%w: allocation descriptor type %d", ErrUnsupported, adType)
	}
	return n, nil
}

// readExtents turns allocation descriptors into the extents of a file of
// the given size.
func (f *FS) readExtents(ads []byte, long bool, part uint16, size int64) ([]extent, error) {
	adSize := 8
	if long {
		adSize = 16
	}
	var (
		out  []extent
		pos  int64
		hops int
	)
	for pos < size {
		if len(ads) < adSize {
			return nil, corrupt("allocation descriptors end after %d of %d bytes", pos, size)
		}
		ad := longAD{length: binary.LittleEndian.Uint32(ads), block: binary.LittleEndian.Uint32(ads[4:]), part: part}
		if long {
			ad = parseLongAD(ads)
		}
		ads = ads[adSize:]
		kind, length := ad.length>>30, int64(ad.length&0x3fffffff)
		if length == 0 {
			return nil, corrupt("allocation descriptors end after %d of %d bytes", pos, size)
		}
		if kind == 3 {
			// The descriptors continue in another block.
			if hops++; hops > maxADHops {
				return nil, corrupt("allocation descriptors do not end")
			}
			b, err := f.block(ad)
			if err != nil {
				return nil, err
			}
			if err := checkTag(b, tagAllocExtent); err != nil {
				return nil, corrupt("allocation extent: %v", err)
			}
			n := binary.LittleEndian.Uint32(b[20:])
			if n > sectorSize-24 {
				return nil, corrupt("allocation extent of %d bytes", n)
			}
			ads = b[24 : 24+n]
			continue
		}
		if len(out) >= maxExtents {
			return nil, fmt.Errorf("%w: more than %d extents", ErrUnsupported, maxExtents)
		}
		if length > size-pos {
			length = size - pos
		}
		e := extent{fileOff: pos, imgOff: -1, length: length}
		if kind == 0 {
			off, err := f.offset(ad.part, ad.block)
			if err != nil {
				return nil, err
			}
			if off+length > f.size {
				return nil, corrupt("an extent ends after the image")
			}
			e.imgOff = off
		}
		out = append(out, e)
		pos += length
	}
	return out, nil
}

// parseTime reads a 12-byte timestamp. An invalid one yields the zero time.
func parseTime(b []byte) time.Time {
	typeTZ := binary.LittleEndian.Uint16(b)
	year := int(int16(binary.LittleEndian.Uint16(b[2:])))
	month, day, hour, min, sec := int(b[4]), int(b[5]), int(b[6]), int(b[7]), int(b[8])
	if year < 1970 || year > 9999 || month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || min > 59 || sec > 59 {
		return time.Time{}
	}
	loc := time.UTC
	if typeTZ>>12 == 1 {
		// A 12-bit signed offset from UTC in minutes; -2047 means unknown.
		off := int(int16(typeTZ<<4)) >> 4
		if off >= -1440 && off <= 1440 {
			loc = time.FixedZone("", off*60)
		}
	}
	return time.Date(year, time.Month(month), day, hour, min, sec, 0, loc).UTC()
}

// readAt reads from the content of a node.
func (f *FS) readAt(n *node, p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fs.ErrInvalid
	}
	if off >= n.size {
		return 0, io.EOF
	}
	want := len(p)
	if int64(want) > n.size-off {
		want = int(n.size - off)
	}
	if n.inline != nil {
		copy(p, n.inline[off:off+int64(want)])
	} else {
		// Find the extent that contains off.
		i := sort.Search(len(n.extents), func(i int) bool {
			return n.extents[i].fileOff+n.extents[i].length > off
		})
		for done := 0; done < want; i++ {
			if i >= len(n.extents) {
				return done, io.ErrUnexpectedEOF
			}
			e := n.extents[i]
			skip := off + int64(done) - e.fileOff
			chunk := want - done
			if int64(chunk) > e.length-skip {
				chunk = int(e.length - skip)
			}
			dst := p[done : done+chunk]
			if e.imgOff < 0 {
				clear(dst)
			} else if _, err := f.r.ReadAt(dst, e.imgOff+skip); err != nil {
				return done, fmt.Errorf("udf: reading %q: %w", n.name, err)
			}
			done += chunk
		}
	}
	if want < len(p) {
		return want, io.EOF
	}
	return want, nil
}

// list returns the entries of a directory, sorted by name.
func (f *FS) list(dir *node) ([]*dirent, error) {
	f.mu.Lock()
	cached, ok := f.dirs[dir]
	f.mu.Unlock()
	if ok {
		return cached, nil
	}
	if dir.size > maxDirBytes {
		return nil, fmt.Errorf("%w: directory of %d bytes", ErrUnsupported, dir.size)
	}
	data := make([]byte, dir.size)
	if _, err := f.readAt(dir, data, 0); err != nil && err != io.EOF {
		return nil, err
	}

	var out []*dirent
	seen := make(map[string]bool)
	for len(data) >= 38 {
		if err := checkTag(data, tagFileIdentifier); err != nil {
			return nil, corrupt("directory %q: %v", dir.name, err)
		}
		characteristics := data[18]
		nameLen := int(data[19])
		implLen := int(binary.LittleEndian.Uint16(data[36:]))
		total := (38 + implLen + nameLen + 3) &^ 3
		if total > len(data) {
			return nil, corrupt("directory %q: an entry is cut off", dir.name)
		}
		const deleted, parent = 1 << 2, 1 << 3
		if characteristics&(deleted|parent) == 0 {
			name := decodeName(data[38+implLen : 38+implLen+nameLen])
			// Names that could not be a path element are left out.
			if name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00") && !seen[name] {
				seen[name] = true
				out = append(out, &dirent{
					name: name,
					dir:  characteristics&(1<<1) != 0,
					icb:  parseLongAD(data[20:36]),
				})
			}
		}
		data = data[total:]
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })

	f.mu.Lock()
	if len(f.dirs) < maxCachedDirs {
		f.dirs[dir] = out
	}
	f.mu.Unlock()
	return out, nil
}

// decodeName decodes an OSTA compressed Unicode string. It returns "" for
// anything that is not valid.
func decodeName(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	switch b[0] {
	case 8:
		r := make([]rune, len(b)-1)
		for i, c := range b[1:] {
			r[i] = rune(c)
		}
		return string(r)
	case 16:
		if len(b)%2 != 1 {
			return ""
		}
		u := make([]uint16, (len(b)-1)/2)
		for i := range u {
			u[i] = binary.BigEndian.Uint16(b[1+2*i:])
		}
		s := string(utf16.Decode(u))
		if strings.ContainsRune(s, '�') {
			return ""
		}
		return s
	}
	return ""
}

// load reads the node of a directory entry once.
func (f *FS) load(e *dirent) (*node, error) {
	e.once.Do(func() {
		e.node, e.err = f.readNode(e.icb, e.name)
		if e.err == nil && e.node.dir != e.dir {
			e.node, e.err = nil, corrupt("%q is a directory in one place and a file in another", e.name)
		}
	})
	return e.node, e.err
}

// find walks a slash-separated path. Names must match exactly.
func (f *FS) find(op, name string) (*node, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: op, Path: name, Err: fs.ErrInvalid}
	}
	n := f.root
	if name == "." {
		return n, nil
	}
	for _, elem := range strings.Split(name, "/") {
		if !n.dir {
			return nil, &fs.PathError{Op: op, Path: name, Err: fs.ErrNotExist}
		}
		entries, err := f.list(n)
		if err != nil {
			return nil, &fs.PathError{Op: op, Path: name, Err: err}
		}
		i := sort.Search(len(entries), func(i int) bool { return entries[i].name >= elem })
		if i == len(entries) || entries[i].name != elem {
			return nil, &fs.PathError{Op: op, Path: name, Err: fs.ErrNotExist}
		}
		if n, err = f.load(entries[i]); err != nil {
			return nil, &fs.PathError{Op: op, Path: name, Err: err}
		}
	}
	return n, nil
}

// Open opens a file or directory.
func (f *FS) Open(name string) (fs.File, error) {
	n, err := f.find("open", name)
	if err != nil {
		return nil, err
	}
	return &file{fs: f, node: n, path: name}, nil
}

// Stat describes a file or directory.
func (f *FS) Stat(name string) (fs.FileInfo, error) {
	n, err := f.find("stat", name)
	if err != nil {
		return nil, err
	}
	return info{n, path.Base(name)}, nil
}

// ReadDir lists a directory, sorted by name.
func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	n, err := f.find("readdir", name)
	if err != nil {
		return nil, err
	}
	if !n.dir {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: errors.New("not a directory")}
	}
	entries, err := f.list(n)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	out := make([]fs.DirEntry, len(entries))
	for i, e := range entries {
		out[i] = dirEntry{f, e}
	}
	return out, nil
}

var (
	_ fs.ReadDirFS = (*FS)(nil)
	_ fs.StatFS    = (*FS)(nil)
)

// info implements fs.FileInfo.
type info struct {
	n    *node
	name string
}

func (i info) Name() string       { return i.name }
func (i info) Size() int64        { return i.n.size }
func (i info) ModTime() time.Time { return i.n.modTime }
func (i info) IsDir() bool        { return i.n.dir }
func (i info) Sys() any           { return nil }
func (i info) Mode() fs.FileMode {
	if i.n.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}

// dirEntry implements fs.DirEntry.
type dirEntry struct {
	fs *FS
	e  *dirent
}

func (d dirEntry) Name() string { return d.e.name }
func (d dirEntry) IsDir() bool  { return d.e.dir }
func (d dirEntry) Type() fs.FileMode {
	if d.e.dir {
		return fs.ModeDir
	}
	return 0
}

func (d dirEntry) Info() (fs.FileInfo, error) {
	n, err := d.fs.load(d.e)
	if err != nil {
		return nil, err
	}
	return info{n, d.e.name}, nil
}

// file is an open file or directory.
type file struct {
	fs   *FS
	node *node
	path string

	mu  sync.Mutex
	pos int64 // read position of a file, or index of the next entry of a directory
}

var (
	_ io.ReaderAt    = (*file)(nil)
	_ io.ReadSeeker  = (*file)(nil)
	_ fs.ReadDirFile = (*file)(nil)
)

func (f *file) Stat() (fs.FileInfo, error) { return info{f.node, path.Base(f.path)}, nil }
func (f *file) Close() error               { return nil }

func (f *file) ReadAt(p []byte, off int64) (int, error) {
	if f.node.dir {
		return 0, &fs.PathError{Op: "read", Path: f.path, Err: errors.New("is a directory")}
	}
	return f.fs.readAt(f.node, p, off)
}

func (f *file) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	n, err := f.ReadAt(p, f.pos)
	f.pos += int64(n)
	if n > 0 && err == io.EOF {
		err = nil
	}
	return n, err
}

func (f *file) Seek(offset int64, whence int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += f.pos
	case io.SeekEnd:
		offset += f.node.size
	default:
		return 0, fs.ErrInvalid
	}
	if offset < 0 {
		return 0, fs.ErrInvalid
	}
	f.pos = offset
	return offset, nil
}

func (f *file) ReadDir(count int) ([]fs.DirEntry, error) {
	if !f.node.dir {
		return nil, &fs.PathError{Op: "readdir", Path: f.path, Err: errors.New("not a directory")}
	}
	entries, err := f.fs.list(f.node)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rest := entries[min(int(f.pos), len(entries)):]
	if count > 0 && len(rest) > count {
		rest = rest[:count]
	}
	if count > 0 && len(rest) == 0 {
		return nil, io.EOF
	}
	f.pos += int64(len(rest))
	out := make([]fs.DirEntry, len(rest))
	for i, e := range rest {
		out[i] = dirEntry{f.fs, e}
	}
	return out, nil
}
