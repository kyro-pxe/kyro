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

// Package wim reads the list of editions out of a Windows image file
// (install.wim or install.esd).
//
// Only the header and the XML description are read. Nothing in the image is
// decompressed, and no file inside it is touched.
package wim

import (
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
)

// maxXML bounds the description. Those of real media are a few kilobytes
// per edition.
const maxXML = 8 << 20

// ErrNotWIM means that the file does not start with the WIM signature.
var ErrNotWIM = errors.New("wim: not a WIM file")

// Edition is one installable image inside a WIM file.
type Edition struct {
	// Index is the number an answer file selects the edition by
	// (/IMAGE/INDEX).
	Index int `json:"index"`
	// Name is the display name, for example "Windows 11 Pro".
	Name string `json:"name"`
	// EditionID is the short name, for example "Professional".
	EditionID string `json:"edition_id,omitempty"`
	// Build is the build number, for example "26100".
	Build string `json:"build,omitempty"`
	// Arch is "x64", "x86", "arm64" or empty.
	Arch string `json:"arch,omitempty"`
}

// IsWIM reports whether r starts with the WIM signature.
func IsWIM(r io.ReaderAt) bool {
	var magic [8]byte
	_, err := r.ReadAt(magic[:], 0)
	return err == nil && string(magic[:]) == "MSWIM\x00\x00\x00"
}

// Editions returns the editions that a WIM file of the given size contains.
func Editions(r io.ReaderAt, size int64) ([]Edition, error) {
	// The header is 208 bytes; the entry for the XML description is at
	// offset 72: seven bytes of size, one of flags, the offset, and the
	// size again.
	var hdr [208]byte
	if _, err := r.ReadAt(hdr[:], 0); err != nil || string(hdr[:8]) != "MSWIM\x00\x00\x00" {
		return nil, ErrNotWIM
	}
	sizeFlags := binary.LittleEndian.Uint64(hdr[72:])
	length, flags := int64(sizeFlags&0x00ffffffffffffff), byte(sizeFlags>>56)
	offset := int64(binary.LittleEndian.Uint64(hdr[80:]))
	const compressed = 0x04
	if flags&compressed != 0 {
		return nil, errors.New("wim: the description is compressed, which is not supported")
	}
	if length < 2 || length > maxXML || offset < 208 || offset > size || length > size-offset {
		return nil, fmt.Errorf("wim: the description (%d bytes at offset %d) does not fit the file", length, offset)
	}
	raw := make([]byte, length)
	if _, err := r.ReadAt(raw, offset); err != nil {
		return nil, fmt.Errorf("wim: reading the description: %w", err)
	}
	// UTF-16, little endian, with a byte order mark.
	if raw[0] != 0xff || raw[1] != 0xfe || len(raw)%2 != 0 {
		return nil, errors.New("wim: the description is not UTF-16")
	}
	units := make([]uint16, (len(raw)-2)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(raw[2+2*i:])
	}

	var doc struct {
		Images []struct {
			Index       int    `xml:"INDEX,attr"`
			Name        string `xml:"NAME"`
			DisplayName string `xml:"DISPLAYNAME"`
			Windows     struct {
				Arch      string `xml:"ARCH"`
				EditionID string `xml:"EDITIONID"`
				Build     string `xml:"VERSION>BUILD"`
			} `xml:"WINDOWS"`
		} `xml:"IMAGE"`
	}
	if err := xml.Unmarshal([]byte(string(utf16.Decode(units))), &doc); err != nil {
		return nil, fmt.Errorf("wim: the description is not valid XML: %v", err)
	}
	if len(doc.Images) > 256 {
		return nil, fmt.Errorf("wim: %d images in one file", len(doc.Images))
	}
	var out []Edition
	for _, im := range doc.Images {
		name := im.DisplayName
		if name == "" {
			name = im.Name
		}
		e := Edition{
			Index:     im.Index,
			Name:      printable(name, 100),
			EditionID: printable(im.Windows.EditionID, 40),
			Build:     digits(im.Windows.Build),
		}
		switch strings.TrimSpace(im.Windows.Arch) {
		case "0":
			e.Arch = "x86"
		case "9":
			e.Arch = "x64"
		case "12":
			e.Arch = "arm64"
		}
		out = append(out, e)
	}
	return out, nil
}

// printable keeps what can be shown in a terminal, up to max bytes. The
// names come from a file that Kyro did not write.
func printable(s string, max int) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == '�' {
			continue
		}
		if b.Len()+len(string(r)) > max {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

func digits(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 10 {
		return ""
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return s
}

// Fake returns a file that Editions reads the given editions from. It is
// for tests, here and in other packages; it is not a WIM that Windows could
// use.
func Fake(editions []Edition, padding int) []byte {
	var x strings.Builder
	x.WriteString("<WIM><TOTALBYTES>0</TOTALBYTES>")
	for _, e := range editions {
		arch := map[string]string{"x86": "0", "x64": "9", "arm64": "12"}[e.Arch]
		fmt.Fprintf(&x, `<IMAGE INDEX="%d"><WINDOWS><ARCH>%s</ARCH><EDITIONID>`, e.Index, arch)
		xml.EscapeText(&x, []byte(e.EditionID))
		fmt.Fprintf(&x, `</EDITIONID><VERSION><MAJOR>10</MAJOR><BUILD>%s</BUILD></VERSION></WINDOWS><NAME>`, e.Build)
		xml.EscapeText(&x, []byte(e.Name))
		x.WriteString("</NAME></IMAGE>")
	}
	x.WriteString("</WIM>")
	desc := []byte{0xff, 0xfe}
	for _, u := range utf16.Encode([]rune(x.String())) {
		desc = append(desc, byte(u), byte(u>>8))
	}
	out := make([]byte, 208+padding)
	copy(out, "MSWIM\x00\x00\x00")
	binary.LittleEndian.PutUint32(out[8:], 208)
	binary.LittleEndian.PutUint64(out[72:], uint64(len(desc)))
	binary.LittleEndian.PutUint64(out[80:], uint64(len(out)))
	binary.LittleEndian.PutUint64(out[88:], uint64(len(desc)))
	return append(out, desc...)
}
