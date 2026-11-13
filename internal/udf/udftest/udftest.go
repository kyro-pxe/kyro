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

// Package udftest builds small UDF images for tests.
//
// The images contain whatever files a test asks for, so no test needs a
// real installation medium. The options select between the on-disk
// variants that the reader in package udf has to understand.
package udftest

import (
	"encoding/binary"
	"sort"
	"strings"
	"time"
)

const (
	sector    = 2048
	partStart = 272  // first sector of the partition
	partNum   = 2989 // the partition number that Windows media use
)

// Options selects the variant of the on-disk format.
type Options struct {
	// LongAD records extents with long allocation descriptors instead of
	// short ones.
	LongAD bool
	// Extended writes extended file entries (UDF 2.00 and later).
	Extended bool
	// Embed stores files of up to 1024 bytes inside their file entry.
	Embed bool
	// ExtentBlocks limits an extent to this many blocks and leaves a gap
	// before the next one. Zero means one extent per file.
	ExtentBlocks int
	// ADsPerEntry is how many allocation descriptors a file entry holds
	// before they continue in a block of their own. Zero means all.
	ADsPerEntry int
	// Holes records blocks that contain only zeros as unallocated extents.
	Holes bool
	// ModTime is the modification time of everything. The zero value
	// means 2026-01-02 03:04:05 UTC.
	ModTime time.Time
}

// Build returns a UDF image with the given files. Keys are slash-separated
// paths; a key that ends in a slash creates an empty directory.
func Build(files map[string][]byte, o Options) []byte {
	if o.ModTime.IsZero() {
		o.ModTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	}
	b := &builder{o: o}
	root := &dir{children: map[string]*dir{}}
	for name, data := range files {
		d := root
		parts := strings.Split(strings.TrimSuffix(name, "/"), "/")
		for i, p := range parts {
			child := d.children[p]
			if child == nil {
				child = &dir{children: map[string]*dir{}}
				d.children[p] = child
			}
			if i == len(parts)-1 && !strings.HasSuffix(name, "/") {
				child.file, child.data = true, data
			}
			d = child
		}
	}

	b.alloc() // block 0: the file set descriptor
	rootICB := b.writeDir(root, 0, true)

	fsd := b.blockBuf(0)
	putLongAD(fsd[400:], sector, rootICB)
	putTag(fsd, 256, 0)

	// Volume structures in front of the partition.
	img := make([]byte, (partStart+len(b.blocks))*sector)
	for i, blk := range b.blocks {
		copy(img[(partStart+i)*sector:], blk)
	}
	copy(img[16*sector:], "\x00BEA01\x01")
	copy(img[17*sector:], "\x00NSR02\x01")
	copy(img[18*sector:], "\x00TEA01\x01")

	anchor := img[256*sector:]
	binary.LittleEndian.PutUint32(anchor[16:], 16*sector)
	binary.LittleEndian.PutUint32(anchor[20:], 257)
	putTag(anchor, 2, 256)

	pvd := img[257*sector:]
	putTag(pvd, 1, 257)

	lvd := img[258*sector:]
	binary.LittleEndian.PutUint32(lvd[212:], sector)
	copy(lvd[216:], "\x00*OSTA UDF Compliant")
	putLongAD(lvd[248:], sector, 0)
	binary.LittleEndian.PutUint32(lvd[264:], 6)
	binary.LittleEndian.PutUint32(lvd[268:], 1)
	lvd[440], lvd[441] = 1, 6
	binary.LittleEndian.PutUint16(lvd[442:], 1)
	binary.LittleEndian.PutUint16(lvd[444:], partNum)
	putTag(lvd, 6, 258)

	pd := img[259*sector:]
	binary.LittleEndian.PutUint16(pd[22:], partNum)
	copy(pd[24:], "\x00+NSR02")
	binary.LittleEndian.PutUint32(pd[188:], partStart)
	binary.LittleEndian.PutUint32(pd[192:], uint32(len(b.blocks)))
	putTag(pd, 5, 259)

	putTag(img[260*sector:], 8, 260)
	return img
}

type dir struct {
	children map[string]*dir
	file     bool
	data     []byte
}

type builder struct {
	o      Options
	blocks [][]byte
}

func (b *builder) alloc() uint32 {
	b.blocks = append(b.blocks, make([]byte, sector))
	return uint32(len(b.blocks) - 1)
}

func (b *builder) blockBuf(n uint32) []byte { return b.blocks[n] }

// writeDir writes a directory and everything below it. It returns the block
// of its file entry.
func (b *builder) writeDir(d *dir, parent uint32, isRoot bool) uint32 {
	self := b.alloc()
	if isRoot {
		parent = self
	}
	names := make([]string, 0, len(d.children))
	for name := range d.children {
		names = append(names, name)
	}
	sort.Strings(names)

	data := fid(nil, "", parent, true, true)
	for _, name := range names {
		child := d.children[name]
		var icb uint32
		if child.file {
			icb = b.alloc()
			b.writeEntry(icb, 5, child.data)
		} else {
			icb = b.writeDir(child, self, false)
		}
		data = fid(data, name, icb, !child.file, false)
	}
	b.writeEntry(self, 4, data)
	return self
}

// fid appends a file identifier descriptor.
func fid(buf []byte, name string, icb uint32, isDir, parent bool) []byte {
	var id []byte
	if name != "" {
		wide := false
		for _, r := range name {
			if r > 0xff {
				wide = true
			}
		}
		if wide {
			id = []byte{16}
			for _, r := range name {
				id = append(id, byte(r>>8), byte(r))
			}
		} else {
			id = []byte{8}
			for _, r := range name {
				id = append(id, byte(r))
			}
		}
	}
	d := make([]byte, (38+len(id)+3)&^3)
	binary.LittleEndian.PutUint16(d[16:], 1)
	if isDir {
		d[18] |= 1 << 1
	}
	if parent {
		d[18] |= 1 << 3
	}
	d[19] = byte(len(id))
	putLongAD(d[20:], sector, icb)
	copy(d[38:], id)
	putTag(d, 257, 0)
	return append(buf, d...)
}

type run struct {
	block  uint32
	length uint32 // bytes
	hole   bool
}

// writeEntry writes a file entry into block icb and the data wherever there
// is room.
func (b *builder) writeEntry(icb uint32, fileType byte, data []byte) {
	fe := b.blockBuf(icb)
	adStart, tagID, mtimeOff := 176, uint16(261), 84
	if b.o.Extended {
		adStart, tagID, mtimeOff = 216, 266, 92
	}
	binary.LittleEndian.PutUint16(fe[20:], 4) // strategy
	binary.LittleEndian.PutUint16(fe[24:], 1)
	fe[27] = fileType
	binary.LittleEndian.PutUint64(fe[56:], uint64(len(data)))
	putTime(fe[mtimeOff:], b.o.ModTime)

	var ads []byte
	flags := uint16(0)
	switch {
	case b.o.Embed && len(data) <= 1024:
		flags = 3
		ads = data
	default:
		adSize := 8
		if b.o.LongAD {
			flags, adSize = 1, 16
		}
		runs := b.writeData(data)
		limit := len(runs)
		if b.o.ADsPerEntry > 0 && b.o.ADsPerEntry < limit {
			limit = b.o.ADsPerEntry
		}
		ads = b.encodeADs(runs[:limit])
		rest := runs[limit:]
		// What does not fit continues in allocation extent blocks.
		tail := &ads
		for len(rest) > 0 {
			n := len(rest)
			if b.o.ADsPerEntry > 0 && b.o.ADsPerEntry < n {
				n = b.o.ADsPerEntry
			}
			blk := b.alloc()
			link := make([]byte, adSize)
			binary.LittleEndian.PutUint32(link, 3<<30|sector)
			binary.LittleEndian.PutUint32(link[4:], blk)
			*tail = append(*tail, link...)

			aed := b.blockBuf(blk)
			body := b.encodeADs(rest[:n])
			rest = rest[n:]
			tail = &body
			defer func(aed []byte, body *[]byte, blk uint32) {
				binary.LittleEndian.PutUint32(aed[20:], uint32(len(*body)))
				copy(aed[24:], *body)
				putTag(aed, 258, blk)
			}(aed, tail, blk)
		}
	}
	binary.LittleEndian.PutUint16(fe[34:], flags)
	if b.o.Extended {
		binary.LittleEndian.PutUint32(fe[212:], uint32(len(ads)))
	} else {
		binary.LittleEndian.PutUint32(fe[172:], uint32(len(ads)))
	}
	copy(fe[adStart:], ads)
	putTag(fe, tagID, icb)
}

// writeData stores data in blocks and returns the runs it occupies.
func (b *builder) writeData(data []byte) []run {
	var runs []run
	per := b.o.ExtentBlocks
	for len(data) > 0 {
		n := len(data)
		if per > 0 && n > per*sector {
			n = per * sector
		}
		chunk := data[:n]
		data = data[n:]
		if b.o.Holes && isZero(chunk) {
			runs = append(runs, run{length: uint32(n), hole: true})
			continue
		}
		first := b.alloc()
		copy(b.blockBuf(first), chunk)
		for off := sector; off < n; off += sector {
			copy(b.blockBuf(b.alloc()), chunk[off:])
		}
		runs = append(runs, run{block: first, length: uint32(n)})
		if per > 0 {
			b.alloc() // a gap, so that extents are not contiguous
		}
	}
	return runs
}

func (b *builder) encodeADs(runs []run) []byte {
	var out []byte
	for _, r := range runs {
		length := r.length
		if r.hole {
			length |= 1 << 30
		}
		if b.o.LongAD {
			ad := make([]byte, 16)
			putLongAD(ad, length, r.block)
			out = append(out, ad...)
		} else {
			ad := make([]byte, 8)
			binary.LittleEndian.PutUint32(ad, length)
			binary.LittleEndian.PutUint32(ad[4:], r.block)
			out = append(out, ad...)
		}
	}
	return out
}

func isZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// putLongAD writes a long allocation descriptor for a block of partition
// reference 0.
func putLongAD(b []byte, length, block uint32) {
	binary.LittleEndian.PutUint32(b, length)
	binary.LittleEndian.PutUint32(b[4:], block)
}

func putTime(b []byte, t time.Time) {
	t = t.UTC()
	binary.LittleEndian.PutUint16(b, 1<<12)
	binary.LittleEndian.PutUint16(b[2:], uint16(t.Year()))
	b[4], b[5], b[6], b[7], b[8] = byte(t.Month()), byte(t.Day()), byte(t.Hour()), byte(t.Minute()), byte(t.Second())
}

// putTag writes a descriptor tag with a valid checksum.
func putTag(b []byte, id uint16, location uint32) {
	binary.LittleEndian.PutUint16(b[0:], id)
	binary.LittleEndian.PutUint16(b[2:], 2)
	b[4] = 0
	binary.LittleEndian.PutUint32(b[12:], location)
	var sum byte
	for i := 0; i < 16; i++ {
		if i != 4 {
			sum += b[i]
		}
	}
	b[4] = sum
}

// FixTag recomputes the checksum of the descriptor tag at the start of b.
// Tests use it after they have changed a descriptor on purpose.
func FixTag(b []byte) {
	putTag(b, binary.LittleEndian.Uint16(b), binary.LittleEndian.Uint32(b[12:]))
}
