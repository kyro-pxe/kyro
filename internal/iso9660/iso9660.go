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

// Package iso9660 finds files in an ISO 9660 image with Joliet names.
//
// It exists for one purpose: to take a handful of driver files out of an
// ISO that is read over the network, a few sectors at a time, without
// downloading all of it. Windows ISOs are UDF and are read by package udf.
//
// Only what that needs is here: the Joliet directory tree, looked up by
// path. No Rock Ridge, no files in several extents, no interleaving.
package iso9660

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
)

const (
	sector = 2048
	// maxDir bounds the size of one directory. The largest directory of
	// the images this is used on has a few dozen entries.
	maxDir = 4 << 20
	// maxDescriptors bounds the search for the Joliet descriptor.
	maxDescriptors = 32
)

// ErrNotFound is returned by Find for a path that is not in the image.
var ErrNotFound = errors.New("no such file in the image")

// Image is an opened ISO 9660 image.
type Image struct {
	r    io.ReaderAt
	size int64
	root entry
}

type entry struct {
	name   string
	offset int64
	size   int64
	dir    bool
}

// Open reads the volume descriptors of an image of the given size and
// fails if it has no Joliet directory tree.
func Open(r io.ReaderAt, size int64) (*Image, error) {
	buf := make([]byte, sector)
	for i := int64(0); i < maxDescriptors; i++ {
		if _, err := r.ReadAt(buf, (16+i)*sector); err != nil {
			return nil, fmt.Errorf("reading the volume descriptors: %w", err)
		}
		if string(buf[1:6]) != "CD001" {
			return nil, errors.New("not an ISO 9660 image")
		}
		switch buf[0] {
		case 255: // terminator
			return nil, errors.New("the image has no Joliet names, which this reader needs")
		case 2: // supplementary volume descriptor; Joliet says so in its escape sequences
			esc := string(buf[88:91])
			if esc != "%/@" && esc != "%/C" && esc != "%/E" {
				continue
			}
			im := &Image{r: r, size: size}
			root, err := im.parse(buf[156:190])
			if err != nil || !root.dir {
				return nil, errors.New("the root directory of the image is damaged")
			}
			im.root = root
			return im, nil
		}
	}
	return nil, errors.New("the image has no Joliet names, which this reader needs")
}

// parse reads one directory record.
func (im *Image) parse(rec []byte) (entry, error) {
	if len(rec) < 34 || int(rec[0]) > len(rec) || int(rec[0]) < 33+int(rec[32]) {
		return entry{}, errors.New("a directory record is damaged")
	}
	e := entry{
		offset: int64(binary.LittleEndian.Uint32(rec[2:])) * sector,
		size:   int64(binary.LittleEndian.Uint32(rec[10:])),
		dir:    rec[25]&0x02 != 0,
	}
	if rec[25]&0x80 != 0 || rec[26] != 0 || rec[27] != 0 {
		return entry{}, errors.New("the image has a file in several pieces, which this reader does not read")
	}
	if e.offset+e.size > im.size {
		return entry{}, errors.New("a directory record points behind the end of the image")
	}
	name := rec[33 : 33+int(rec[32])]
	switch {
	case len(name) == 1 && name[0] == 0:
		e.name = "."
	case len(name) == 1 && name[0] == 1:
		e.name = ".."
	case len(name)%2 != 0:
		return entry{}, errors.New("a file name of the image is damaged")
	default:
		units := make([]uint16, len(name)/2)
		for i := range units {
			units[i] = binary.BigEndian.Uint16(name[2*i:])
		}
		e.name = string(utf16.Decode(units))
		// Files carry a version: "NAME.EXT;1".
		if i := strings.LastIndexByte(e.name, ';'); i >= 0 && !e.dir {
			e.name = e.name[:i]
		}
	}
	return e, nil
}

// list reads a directory.
func (im *Image) list(dir entry) ([]entry, error) {
	if dir.size > maxDir {
		return nil, errors.New("a directory of the image is larger than this reader accepts")
	}
	data := make([]byte, dir.size)
	if _, err := im.r.ReadAt(data, dir.offset); err != nil {
		return nil, fmt.Errorf("reading a directory: %w", err)
	}
	var out []entry
	for pos := 0; pos < len(data); {
		n := int(data[pos])
		if n == 0 {
			// Records do not cross sectors; the rest of this one is empty.
			pos = (pos/sector + 1) * sector
			continue
		}
		if pos+n > len(data) {
			return nil, errors.New("a directory record is damaged")
		}
		e, err := im.parse(data[pos : pos+n])
		if err != nil {
			return nil, err
		}
		if e.name != "." && e.name != ".." {
			out = append(out, e)
		}
		pos += n
	}
	return out, nil
}

// Find returns where the file at path is in the image. The path uses
// slashes; upper and lower case are not told apart, as on Windows, where
// these files are used.
func (im *Image) Find(path string) (offset, size int64, err error) {
	cur := im.root
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == "" || !cur.dir {
			return 0, 0, fmt.Errorf("%s: %w", path, ErrNotFound)
		}
		entries, err := im.list(cur)
		if err != nil {
			return 0, 0, err
		}
		found := false
		for _, e := range entries {
			if strings.EqualFold(e.name, part) && e.dir == (i < len(parts)-1) {
				cur, found = e, true
				break
			}
		}
		if !found {
			return 0, 0, fmt.Errorf("%s: %w", path, ErrNotFound)
		}
	}
	return cur.offset, cur.size, nil
}

// ReadFile returns the content of the file at path, if it is not larger
// than limit.
func (im *Image) ReadFile(path string, limit int64) ([]byte, error) {
	offset, size, err := im.Find(path)
	if err != nil {
		return nil, err
	}
	if size > limit {
		return nil, fmt.Errorf("%s has %d bytes, more than the %d that were expected", path, size, limit)
	}
	data := make([]byte, size)
	if _, err := im.r.ReadAt(data, offset); err != nil && !(err == io.EOF && size == 0) {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return data, nil
}
