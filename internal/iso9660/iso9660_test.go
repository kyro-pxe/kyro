// Copyright (C) 2026 Lukas Koch and Kyro contributors
// SPDX-License-Identifier: GPL-3.0-or-later

package iso9660

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// fixture is testdata/tree.iso.gz, made with
//
//	genisoimage -J -R -V KYROTEST -o tree.iso tree
//
// the program that also makes the image this reader is used on. The tree:
//
//	NetKVM/w11/amd64/netkvm.inf   "the inf\r\n"
//	NetKVM/w11/amd64/netkvm.sys   5000 bytes, byte i is i%251
//	NetKVM/w11/amd64/empty.cat    empty
//	Dir With Spaces/A File.txt    "spaces"
//	license.txt                   "license"
//	many/a-rather-long-file-name-number-<1..90>.bin   "file <n>"
func fixture(t testing.TB) []byte {
	t.Helper()
	f, err := os.Open("testdata/tree.iso.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func open(t testing.TB, data []byte) *Image {
	t.Helper()
	im, err := Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return im
}

func TestReadFile(t *testing.T) {
	im := open(t, fixture(t))
	sys := make([]byte, 5000)
	for i := range sys {
		sys[i] = byte(i % 251)
	}
	want := map[string]string{
		"NetKVM/w11/amd64/netkvm.inf": "the inf\r\n",
		"NetKVM/w11/amd64/netkvm.sys": string(sys),
		"NetKVM/w11/amd64/empty.cat":  "",
		"Dir With Spaces/A File.txt":  "spaces",
		"license.txt":                 "license",
		// Upper and lower case are the same file.
		"NETKVM/W11/AMD64/NETKVM.INF": "the inf\r\n",
		"netkvm/w11/amd64/netkvm.inf": "the inf\r\n",
	}
	// The directory "many" is longer than one sector.
	for i := 1; i <= 90; i++ {
		want[fmt.Sprintf("many/a-rather-long-file-name-number-%d.bin", i)] = fmt.Sprintf("file %d", i)
	}
	for path, content := range want {
		got, err := im.ReadFile(path, 1<<20)
		if err != nil || string(got) != content {
			t.Errorf("%s: %d bytes, %v; want %d bytes", path, len(got), err, len(content))
		}
	}
	if _, size, err := im.Find("NetKVM/w11/amd64/netkvm.sys"); err != nil || size != 5000 {
		t.Errorf("Find: size %d, %v", size, err)
	}
}

func TestNotFound(t *testing.T) {
	im := open(t, fixture(t))
	for _, path := range []string{
		"", "/", "missing.txt", "NetKVM", "NetKVM/w11", "NetKVM/w11/amd64/", "NetKVM/w11/amd64/missing.sys",
		"license.txt/x", "/license.txt", "NetKVM//w11", "../license.txt", "NetKVM/../license.txt", "license.txt;1",
	} {
		if _, _, err := im.Find(path); !errors.Is(err, ErrNotFound) {
			t.Errorf("Find(%q): %v", path, err)
		}
	}
	if _, err := im.ReadFile("NetKVM/w11/amd64/netkvm.sys", 4999); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("a file over the limit: %v", err)
	}
}

func TestOpenRejects(t *testing.T) {
	good := fixture(t)
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"empty", nil, "reading the volume descriptors"},
		{"short", good[:17*sector-1], "reading the volume descriptors"},
		{"zeros", make([]byte, 40*sector), "not an ISO 9660 image"},
		{"text", bytes.Repeat([]byte("not an image "), 10000), "not an ISO 9660 image"},
	}
	// Without Joliet: the supplementary descriptor turned into something else.
	plain := bytes.Clone(good)
	for i := 16; i < 16+maxDescriptors; i++ {
		if d := plain[i*sector:]; d[0] == 2 {
			d[88] = 'x'
		}
	}
	tests = append(tests, struct {
		name string
		data []byte
		want string
	}{"no Joliet", plain, "no Joliet names"})
	for _, tt := range tests {
		if _, err := Open(bytes.NewReader(tt.data), int64(len(tt.data))); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want an error with %q", tt.name, err, tt.want)
		}
	}
}

// An image that was cut off must give errors, not content.
func TestTruncated(t *testing.T) {
	good := fixture(t)
	cut := good[:len(good)/2]
	im, err := Open(bytes.NewReader(cut), int64(len(cut)))
	if err != nil {
		return // also fine: the tree itself was behind the cut
	}
	for _, path := range []string{"NetKVM/w11/amd64/netkvm.sys", "license.txt", "many/a-rather-long-file-name-number-90.bin"} {
		if data, err := im.ReadFile(path, 1<<20); err == nil {
			// What is before the cut may be read; it must then be right.
			full, _ := open(t, good).ReadFile(path, 1<<20)
			if !bytes.Equal(data, full) {
				t.Errorf("%s: wrong content from a truncated image", path)
			}
		}
	}
}

// FuzzFind changes bytes of the image and looks files up. Whatever the
// image says, the reader must not panic or read outside of it.
func FuzzFind(f *testing.F) {
	good := fixture(f)
	f.Add(uint32(16*sector+156+2), byte(0xff), "license.txt")
	f.Add(uint32(18*sector), byte(0), "NetKVM/w11/amd64/netkvm.sys")
	f.Fuzz(func(t *testing.T, at uint32, value byte, path string) {
		data := bytes.Clone(good)
		data[int(at)%len(data)] = value
		im, err := Open(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return
		}
		im.ReadFile(path, 1<<20)
		im.ReadFile("NetKVM/w11/amd64/netkvm.sys", 1<<20)
		im.ReadFile("many/a-rather-long-file-name-number-77.bin", 1<<20)
	})
}
