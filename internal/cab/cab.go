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

// Package cab writes cabinet files (.cab) without compression.
//
// Kyro has to hand a directory tree to Windows PE as one file, and the one
// archive format that Windows PE can unpack with a program of its own
// (expand.exe) is the cabinet. Everything is stored: the files are driver
// packages of a few megabytes that travel over a local network once.
//
// The format is Microsoft's published "Cabinet File Format". A cabinet made
// here has one folder, in which all files follow each other and are cut
// into blocks of 32768 bytes.
package cab

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	blockSize  = 32768
	headerSize = 36
	folderSize = 8
	fileFixed  = 16
	dataHeader = 8

	// The limits of the format for one folder in one cabinet.
	maxFiles  = 65535
	maxBlocks = 65535
	// MaxName is the longest file name a cabinet can hold.
	MaxName = 255
)

// File is one file to store. Name is its path inside the cabinet, with
// backslashes between directories, as Windows will create them.
type File struct {
	Name    string
	Size    int64
	ModTime time.Time
	// Open returns the content. It is called once, when the file's turn
	// has come, and must deliver exactly Size bytes.
	Open func() (io.ReadCloser, error)
}

// ErrChanged is returned when a file does not deliver the size it was
// announced with. The sizes are written before the content, so the cabinet
// is damaged at that point and must be thrown away.
var ErrChanged = errors.New("a file changed while the cabinet was written")

// Size returns how many bytes Write will produce for these files, or an
// error if they cannot be stored in one cabinet.
func Size(files []File) (int64, error) {
	total, _, err := layout(files)
	return total, err
}

// layout checks the files and returns the size of the cabinet and the
// number of data blocks.
func layout(files []File) (total int64, blocks int, err error) {
	if len(files) == 0 {
		return 0, 0, errors.New("a cabinet needs at least one file")
	}
	if len(files) > maxFiles {
		return 0, 0, fmt.Errorf("%d files: a cabinet holds at most %d", len(files), maxFiles)
	}
	var content, names int64
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		if err := CheckName(f.Name); err != nil {
			return 0, 0, err
		}
		lower := strings.ToLower(f.Name)
		if seen[lower] {
			return 0, 0, fmt.Errorf("%q is in the cabinet twice (Windows does not tell upper from lower case)", f.Name)
		}
		seen[lower] = true
		if f.Size < 0 || f.Size > 0x7fffffff {
			return 0, 0, fmt.Errorf("%q: %d bytes do not fit into a cabinet", f.Name, f.Size)
		}
		content += f.Size
		names += int64(len(f.Name)) + 1
	}
	n := (content + blockSize - 1) / blockSize
	if n > maxBlocks {
		return 0, 0, fmt.Errorf("%d bytes do not fit into one cabinet", content)
	}
	total = headerSize + folderSize + int64(len(files))*fileFixed + names + n*dataHeader + content
	if total > 0x7fffffff {
		return 0, 0, fmt.Errorf("%d bytes do not fit into one cabinet", content)
	}
	return total, int(n), nil
}

// CheckName accepts what can be a path of a file on Windows and can be
// written into a cabinet without a flag for another character set.
// Directories are separated by backslashes.
func CheckName(name string) error {
	if name == "" || len(name) > MaxName {
		return fmt.Errorf("file name %q: empty or longer than %d bytes", name, MaxName)
	}
	for _, part := range strings.Split(name, `\`) {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || strings.HasPrefix(part, " ") {
			return fmt.Errorf("file name %q: %q cannot be the name of a file or directory on Windows", name, part)
		}
		for _, c := range []byte(part) {
			if c < 0x20 || c > 0x7e || strings.IndexByte(`/:*?"<>|`, c) >= 0 {
				return fmt.Errorf("file name %q: only printable ASCII characters other than / : * ? \" < > | can be used", name)
			}
		}
	}
	return nil
}

// Write writes a cabinet with the given files to w, in their order.
func Write(w io.Writer, files []File) error {
	total, blocks, err := layout(files)
	if err != nil {
		return err
	}
	le := binary.LittleEndian

	var names int
	for _, f := range files {
		names += len(f.Name) + 1
	}
	firstFile := headerSize + folderSize
	firstData := firstFile + len(files)*fileFixed + names

	head := make([]byte, 0, firstData)
	head = append(head, 'M', 'S', 'C', 'F')
	head = le.AppendUint32(head, 0)                  // reserved
	head = le.AppendUint32(head, uint32(total))      // size of the cabinet
	head = le.AppendUint32(head, 0)                  // reserved
	head = le.AppendUint32(head, uint32(firstFile))  // offset of the first file entry
	head = le.AppendUint32(head, 0)                  // reserved
	head = append(head, 3, 1)                        // version 1.3
	head = le.AppendUint16(head, 1)                  // folders
	head = le.AppendUint16(head, uint16(len(files))) // files
	head = le.AppendUint16(head, 0)                  // flags
	head = le.AppendUint16(head, 0)                  // set ID
	head = le.AppendUint16(head, 0)                  // number of this cabinet in its set

	head = le.AppendUint32(head, uint32(firstData)) // offset of the first data block
	head = le.AppendUint16(head, uint16(blocks))
	head = le.AppendUint16(head, 0) // compression: none

	var offset int64
	for _, f := range files {
		date, clock := dosTime(f.ModTime)
		head = le.AppendUint32(head, uint32(f.Size))
		head = le.AppendUint32(head, uint32(offset)) // offset inside the folder
		head = le.AppendUint16(head, 0)              // folder
		head = le.AppendUint16(head, date)
		head = le.AppendUint16(head, clock)
		head = le.AppendUint16(head, 0x20) // attribute: archive
		head = append(head, f.Name...)
		head = append(head, 0)
		offset += f.Size
	}
	if _, err := w.Write(head); err != nil {
		return err
	}

	// The content of all files, one after the other, in blocks.
	block := make([]byte, dataHeader, dataHeader+blockSize)
	flush := func() error {
		data := block[dataHeader:]
		if len(data) == 0 {
			return nil
		}
		le.PutUint16(block[4:], uint16(len(data))) // stored size
		le.PutUint16(block[6:], uint16(len(data))) // size once unpacked
		le.PutUint32(block[0:], checksum(block[4:]))
		_, err := w.Write(block)
		block = block[:dataHeader]
		return err
	}
	for _, f := range files {
		if err := copyFile(f, &block, flush); err != nil {
			return err
		}
	}
	return flush()
}

// copyFile appends the content of one file to the current block and
// writes out every block that fills up.
func copyFile(f File, block *[]byte, flush func() error) error {
	if f.Size == 0 {
		return nil
	}
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	left := f.Size
	for left > 0 {
		b := *block
		room := dataHeader + blockSize - len(b)
		if int64(room) > left {
			room = int(left)
		}
		n, err := io.ReadFull(r, b[len(b):len(b)+room])
		*block = b[:len(b)+n]
		left -= int64(n)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return fmt.Errorf("%s: %w", f.Name, ErrChanged)
			}
			return err
		}
		if len(*block) == dataHeader+blockSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	// More content than announced is a change as well.
	var one [1]byte
	if n, _ := r.Read(one[:]); n > 0 {
		return fmt.Errorf("%s: %w", f.Name, ErrChanged)
	}
	return nil
}

// checksum is the checksum of a data block as the cabinet format defines
// it: the block is read as 32-bit little-endian words that are combined
// with exclusive or, and what is left over at the end is read in the
// opposite order.
func checksum(b []byte) uint32 {
	var sum uint32
	for len(b) >= 4 {
		sum ^= binary.LittleEndian.Uint32(b)
		b = b[4:]
	}
	var rest uint32
	switch len(b) {
	case 3:
		rest = uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
	case 2:
		rest = uint32(b[0])<<8 | uint32(b[1])
	case 1:
		rest = uint32(b[0])
	}
	return sum ^ rest
}

// dosTime returns a time as the cabinet stores it: local time without a
// zone, in steps of two seconds, from 1980 to 2107. Kyro writes UTC.
func dosTime(t time.Time) (date, clock uint16) {
	t = t.UTC()
	year := t.Year()
	if year < 1980 {
		return 1<<5 | 1, 0 // 1980-01-01 00:00:00
	}
	if year > 2107 {
		year = 2107
	}
	date = uint16(year-1980)<<9 | uint16(t.Month())<<5 | uint16(t.Day())
	clock = uint16(t.Hour())<<11 | uint16(t.Minute())<<5 | uint16(t.Second()/2)
	return date, clock
}
