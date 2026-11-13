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

package wim

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

var sample = []Edition{
	{Index: 1, Name: "Windows 11 Home", EditionID: "Core", Build: "26100", Arch: "x64"},
	{Index: 2, Name: "Windows 11 Pro", EditionID: "Professional", Build: "26100", Arch: "x64"},
	{Index: 3, Name: "Windows 11 <Enterprise> & Co", EditionID: "Enterprise", Build: "26100", Arch: "arm64"},
}

func TestEditions(t *testing.T) {
	data := Fake(sample, 4096)
	got, err := Editions(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, sample) {
		t.Errorf("editions = %+v", got)
	}
	if !IsWIM(bytes.NewReader(data)) || IsWIM(bytes.NewReader([]byte("MZ not a wim"))) || IsWIM(bytes.NewReader(nil)) {
		t.Error("IsWIM is wrong")
	}
}

func TestEditionsRejects(t *testing.T) {
	good := Fake(sample, 0)
	tests := []struct {
		name string
		edit func([]byte) []byte
		want string
	}{
		{"not a WIM", func(b []byte) []byte { return []byte(strings.Repeat("x", 300)) }, "not a WIM"},
		{"too short", func(b []byte) []byte { return b[:100] }, "not a WIM"},
		{"compressed description", func(b []byte) []byte { b[79] = 0x04; return b }, "compressed"},
		{"description beyond the file", func(b []byte) []byte { binary.LittleEndian.PutUint64(b[80:], 1<<40); return b }, "does not fit"},
		{"description inside the header", func(b []byte) []byte { binary.LittleEndian.PutUint64(b[80:], 10); return b }, "does not fit"},
		{"huge description", func(b []byte) []byte { binary.LittleEndian.PutUint64(b[72:], 1<<40); return b }, "does not fit"},
		{"description cut off", func(b []byte) []byte { return b[:len(b)-10] }, "does not fit"},
		{"not UTF-16", func(b []byte) []byte { b[208] = '<'; return b }, "UTF-16"},
		{"broken XML", func(b []byte) []byte { b[210+2] = '<'; return b }, "XML"},
	}
	for _, tt := range tests {
		data := tt.edit(bytes.Clone(good))
		_, err := Editions(bytes.NewReader(data), int64(len(data)))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want one that mentions %q", tt.name, err, tt.want)
		}
	}
	if _, err := Editions(bytes.NewReader(nil), 0); !errors.Is(err, ErrNotWIM) {
		t.Errorf("empty file: %v", err)
	}
}

func TestNamesAreCleaned(t *testing.T) {
	data := Fake([]Edition{{Index: 1, Name: "Evil\x1b[2J\x07 " + strings.Repeat("x", 300), Build: "26100; id", EditionID: "A\nB"}}, 0)
	got, err := Editions(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	e := got[0]
	if strings.ContainsAny(e.Name+e.EditionID, "\x1b\x07\n") || len(e.Name) > 100 || e.Build != "" {
		t.Errorf("edition = %q %q %q", e.Name, e.EditionID, e.Build)
	}
}

// TestRealWIM reads the editions of a real image when KYRO_TEST_WIM names
// one.
func TestRealWIM(t *testing.T) {
	path := os.Getenv("KYRO_TEST_WIM")
	if path == "" {
		t.Skip("KYRO_TEST_WIM is not set")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	got, err := Editions(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Error("no editions")
	}
	t.Logf("%+v", got)
}

func FuzzEditions(f *testing.F) {
	f.Add(Fake(sample, 0))
	f.Add(Fake(nil, 16))
	f.Add([]byte("MSWIM\x00\x00\x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := Editions(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return
		}
		for _, e := range got {
			for _, r := range e.Name + e.EditionID + e.Build + e.Arch {
				if r < 0x20 || r == 0x7f {
					t.Fatalf("control character in %q %q %q", e.Name, e.EditionID, e.Build)
				}
			}
		}
	})
}
