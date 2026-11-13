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

package udf_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kyro-pxe/kyro/internal/udf"
	"github.com/kyro-pxe/kyro/internal/udf/udftest"
)

// pattern returns n bytes that differ from block to block.
func pattern(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i/251) + byte(i) + seed
	}
	return b
}

func testFiles() map[string][]byte {
	sparse := make([]byte, 5*2048)
	copy(sparse[2*2048:], pattern(2048, 9))
	return map[string][]byte{
		"setup.exe":            []byte("MZ"),
		"empty.txt":            {},
		"sources/boot.wim":     pattern(3*2048+17, 1),
		"sources/install.wim":  pattern(40*2048+5, 2),
		"sources/EI.CFG":       []byte("[Channel]\r\nRetail\r\n"),
		"sources/sparse.bin":   sparse,
		"sources/sxs/a.cab":    pattern(2048, 3),
		"efi/boot/bootx64.efi": pattern(1000, 4),
		"Größe/日本語.txt":        []byte("unicode names"),
		"emptydir/":            nil,
	}
}

var variants = map[string]udftest.Options{
	"short descriptors":      {},
	"long descriptors":       {LongAD: true},
	"extended entries":       {Extended: true},
	"embedded files":         {Embed: true},
	"many extents":           {ExtentBlocks: 3},
	"many extents, long":     {ExtentBlocks: 2, LongAD: true, Extended: true},
	"continued descriptors":  {ExtentBlocks: 1, ADsPerEntry: 4},
	"continued, long":        {ExtentBlocks: 1, ADsPerEntry: 3, LongAD: true},
	"holes":                  {ExtentBlocks: 1, Holes: true},
	"everything at once":     {ExtentBlocks: 1, ADsPerEntry: 5, Holes: true, Embed: true, Extended: true, LongAD: true},
	"embedded and continued": {ExtentBlocks: 2, ADsPerEntry: 2, Embed: true},
}

func open(t testing.TB, img []byte) *udf.FS {
	t.Helper()
	fsys, err := udf.Open(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return fsys
}

func TestReadsEveryVariant(t *testing.T) {
	files := testFiles()
	for name, opts := range variants {
		t.Run(name, func(t *testing.T) {
			fsys := open(t, udftest.Build(files, opts))
			var want []string
			for path, data := range files {
				if strings.HasSuffix(path, "/") {
					continue
				}
				want = append(want, path)
				got, err := fs.ReadFile(fsys, path)
				if err != nil {
					t.Errorf("ReadFile(%q): %v", path, err)
				} else if !bytes.Equal(got, data) {
					t.Errorf("ReadFile(%q): %d bytes, want %d; content differs", path, len(got), len(data))
				}
			}
			// The standard library's own check of an fs.FS: listing, Stat,
			// Open, ReadAt, Seek and their consistency with each other.
			if err := fstest.TestFS(fsys, want...); err != nil {
				t.Error(err)
			}
			if entries, err := fs.ReadDir(fsys, "emptydir"); err != nil || len(entries) != 0 {
				t.Errorf("ReadDir(emptydir) = %v, %v", entries, err)
			}
		})
	}
}

func TestMetadata(t *testing.T) {
	when := time.Date(2025, 9, 13, 21, 17, 33, 0, time.UTC)
	fsys := open(t, udftest.Build(testFiles(), udftest.Options{ModTime: when}))

	info, err := fs.Stat(fsys, "sources/install.wim")
	if err != nil {
		t.Fatal(err)
	}
	if info.Name() != "install.wim" || info.Size() != 40*2048+5 || info.IsDir() || !info.Mode().IsRegular() {
		t.Errorf("file info = %v %d %v", info.Name(), info.Size(), info.Mode())
	}
	if !info.ModTime().Equal(when) {
		t.Errorf("mod time = %v, want %v", info.ModTime(), when)
	}
	if info.Mode().Perm()&0o222 != 0 {
		t.Errorf("mode %v allows writing", info.Mode())
	}

	info, err = fs.Stat(fsys, "sources")
	if err != nil || !info.IsDir() {
		t.Errorf("Stat(sources) = %v, %v", info, err)
	}
	info, err = fs.Stat(fsys, ".")
	if err != nil || !info.IsDir() || info.Name() != "." {
		t.Errorf("Stat(.) = %v, %v", info, err)
	}

	entries, err := fs.ReadDir(fsys, "sources")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if got := strings.Join(names, " "); got != "EI.CFG boot.wim install.wim sparse.bin sxs" {
		t.Errorf("listing = %q", got)
	}
}

func TestLookupErrors(t *testing.T) {
	fsys := open(t, udftest.Build(testFiles(), udftest.Options{}))
	for _, name := range []string{
		"missing", "sources/missing", "setup.exe/x", "sources/ei.cfg", // names are exact
	} {
		if _, err := fsys.Open(name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Open(%q) = %v, want not exist", name, err)
		}
	}
	for _, name := range []string{"", "/setup.exe", "sources/", "../x", "sources/../setup.exe", "./setup.exe"} {
		if _, err := fsys.Open(name); !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("Open(%q) = %v, want invalid", name, err)
		}
	}
	if _, err := fs.ReadDir(fsys, "setup.exe"); err == nil {
		t.Error("ReadDir of a file succeeded")
	}
}

func TestReadAt(t *testing.T) {
	data := pattern(40*2048+5, 2)
	for name, opts := range variants {
		t.Run(name, func(t *testing.T) {
			fsys := open(t, udftest.Build(testFiles(), opts))
			f, err := fsys.Open("sources/install.wim")
			if err != nil {
				t.Fatal(err)
			}
			r := f.(io.ReaderAt)
			size := int64(len(data))
			for _, c := range []struct{ off, n int64 }{
				{0, 1}, {0, 2048}, {2047, 2}, {2048, 4096}, {3 * 2048, 2049},
				{size - 5, 5}, {size - 5, 100}, {size, 10}, {size + 99, 1}, {0, size},
			} {
				buf := make([]byte, c.n)
				n, err := r.ReadAt(buf, c.off)
				want := data[min(c.off, size):min(c.off+c.n, size)]
				if !bytes.Equal(buf[:n], want) {
					t.Errorf("ReadAt(%d, %d): wrong data", c.off, c.n)
				}
				if (int64(n) < c.n) != (err == io.EOF) || (err != nil && err != io.EOF) {
					t.Errorf("ReadAt(%d, %d) = %d, %v", c.off, c.n, n, err)
				}
			}
			if _, err := r.ReadAt(make([]byte, 1), -1); err == nil {
				t.Error("ReadAt with a negative offset succeeded")
			}
		})
	}
}

func TestNotUDF(t *testing.T) {
	for name, img := range map[string][]byte{
		"empty":     {},
		"too short": make([]byte, 100*2048),
		"zeros":     make([]byte, 600*2048),
		"text":      bytes.Repeat([]byte("not an image "), 100000),
	} {
		if _, err := udf.Open(bytes.NewReader(img), int64(len(img))); !errors.Is(err, udf.ErrNotUDF) {
			t.Errorf("%s: err = %v, want ErrNotUDF", name, err)
		}
	}
}

// TestRejectsBrokenImages changes one thing in a good image and expects an
// error, not a panic and not wrong data.
func TestRejectsBrokenImages(t *testing.T) {
	const sector = 2048
	const lvd, pd, part = 258 * sector, 259 * sector, 272 * sector
	tests := []struct {
		name string
		edit func(img []byte)
		want error
	}{
		{"block size of 512", func(img []byte) {
			binary.LittleEndian.PutUint32(img[lvd+212:], 512)
			udftest.FixTag(img[lvd:])
		}, udf.ErrUnsupported},
		{"metadata partition map", func(img []byte) {
			img[lvd+440] = 2
			udftest.FixTag(img[lvd:])
		}, udf.ErrUnsupported},
		{"partition map names a missing partition", func(img []byte) {
			binary.LittleEndian.PutUint16(img[lvd+444:], 7)
			udftest.FixTag(img[lvd:])
		}, udf.ErrCorrupt},
		{"partition longer than the image", func(img []byte) {
			binary.LittleEndian.PutUint32(img[pd+192:], 1<<30)
			udftest.FixTag(img[pd:])
		}, udf.ErrCorrupt},
		{"bad checksum on the logical volume", func(img []byte) { img[lvd+4]++ }, udf.ErrCorrupt},
		{"file set descriptor overwritten", func(img []byte) { clear(img[part : part+16]) }, udf.ErrCorrupt},
		{"root entry overwritten", func(img []byte) { clear(img[part+sector : part+2*sector]) }, udf.ErrCorrupt},
		{"root claims to be huge", func(img []byte) {
			binary.LittleEndian.PutUint64(img[part+sector+56:], 1<<50)
			udftest.FixTag(img[part+sector:])
		}, udf.ErrCorrupt},
		{"root has strategy 4096", func(img []byte) {
			binary.LittleEndian.PutUint16(img[part+sector+20:], 4096)
			udftest.FixTag(img[part+sector:])
		}, udf.ErrUnsupported},
		{"root is a symbolic link", func(img []byte) {
			img[part+sector+27] = 12
			udftest.FixTag(img[part+sector:])
		}, udf.ErrUnsupported},
		{"descriptors longer than the block", func(img []byte) {
			binary.LittleEndian.PutUint32(img[part+sector+172:], 4000)
			udftest.FixTag(img[part+sector:])
		}, udf.ErrCorrupt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img := udftest.Build(testFiles(), udftest.Options{})
			tt.edit(img)
			_, err := udf.Open(bytes.NewReader(img), int64(len(img)))
			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestTruncatedImage(t *testing.T) {
	img := udftest.Build(testFiles(), udftest.Options{})
	// Cut the image in the middle of the file data. Whatever still opens
	// must not return data from beyond the end.
	cut := img[:len(img)-20*2048]
	fsys, err := udf.Open(bytes.NewReader(cut), int64(len(cut)))
	if err != nil {
		if !errors.Is(err, udf.ErrCorrupt) {
			t.Errorf("err = %v, want ErrCorrupt", err)
		}
		return
	}
	walk(fsys, 1<<30)
}

// walk reads everything in an image, within a budget, and returns how many
// bytes it read. It must terminate for any image.
func walk(fsys *udf.FS, budget int64) int64 {
	var total int64
	// A damaged image can contain a directory that lists itself, so the
	// number of entries looked at is limited, not only the depth.
	visits := 0
	var visit func(dir string, depth int)
	visit = func(dir string, depth int) {
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil || depth > 8 {
			return
		}
		for _, e := range entries {
			if visits++; visits > 500 || total > budget {
				return
			}
			name := e.Name()
			if dir != "." {
				name = dir + "/" + name
			}
			if !fs.ValidPath(name) {
				panic(fmt.Sprintf("listing of %q contains the invalid name %q", dir, e.Name()))
			}
			if e.IsDir() {
				visit(name, depth+1)
				continue
			}
			f, err := fsys.Open(name)
			if err != nil {
				continue
			}
			n, _ := io.Copy(io.Discard, io.LimitReader(f, budget))
			total += n
			if info, err := f.Stat(); err == nil && n > info.Size() {
				panic(fmt.Sprintf("%q: read %d bytes of a file of %d", name, n, info.Size()))
			}
		}
	}
	visit(".", 0)
	return total
}

// FuzzOpen damages good images and gives them to the reader. It must return
// errors, never panic, never loop, and never read more from a file than the
// file's size.
//
// The fuzzer does not get the image itself, which is half a megabyte of
// mostly zeros. It gets a list of changes to one of a few good images: five
// bytes each, naming a sector after the anchor, an offset in it, the new
// value, and whether to repair the checksum of the sector's descriptor tag
// afterwards. That way a mutation is a change to a structure, and a
// damaged descriptor still gets past the checksum.
func FuzzOpen(f *testing.F) {
	small := map[string][]byte{
		"a.txt":       []byte("hello"),
		"dir/b.bin":   pattern(3*2048, 1),
		"dir/sub/c":   pattern(100, 2),
		"dir/empty/":  nil,
		"sources/big": pattern(9*2048+1, 3),
	}
	var bases [][]byte
	for _, opts := range []udftest.Options{
		{}, {LongAD: true, Extended: true}, {Embed: true},
		{ExtentBlocks: 1, ADsPerEntry: 2}, {ExtentBlocks: 1, Holes: true, LongAD: true},
	} {
		bases = append(bases, udftest.Build(small, opts))
	}
	for i := range bases {
		f.Add(uint8(i), []byte{})
		f.Add(uint8(i), []byte{2, 212, 0, 0, 1})    // block size in the logical volume
		f.Add(uint8(i), []byte{17, 56, 0, 0xff, 1}) // size of the root directory
		f.Add(uint8(i), []byte{17, 176, 0, 0xff, 1})
	}
	f.Fuzz(func(t *testing.T, base uint8, changes []byte) {
		img := bytes.Clone(bases[int(base)%len(bases)])
		sectors := len(img)/2048 - 256
		for ; len(changes) >= 5; changes = changes[5:] {
			sector := img[(256+int(changes[0])%sectors)*2048:][:2048]
			sector[int(binary.LittleEndian.Uint16(changes[1:]))%2048] = changes[3]
			if changes[4]&1 != 0 {
				udftest.FixTag(sector)
			}
		}
		fsys, err := udf.Open(bytes.NewReader(img), int64(len(img)))
		if err != nil {
			return
		}
		walk(fsys, 4<<20)
	})
}

// TestRealISO compares the reader with a list of checksums. It runs only
// when KYRO_TEST_ISO names an image and KYRO_TEST_ISO_SUMS a file in the
// format of sha256sum that was made from the mounted image.
func TestRealISO(t *testing.T) {
	iso, sums := os.Getenv("KYRO_TEST_ISO"), os.Getenv("KYRO_TEST_ISO_SUMS")
	if iso == "" || sums == "" {
		t.Skip("KYRO_TEST_ISO and KYRO_TEST_ISO_SUMS are not set")
	}
	f, err := os.Open(iso)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := udf.Open(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	list, err := os.ReadFile(sums)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, line := range strings.Split(strings.TrimSpace(string(list)), "\n") {
		want, name, ok := strings.Cut(line, "  ")
		if !ok {
			t.Fatalf("bad line %q", line)
		}
		file, err := fsys.Open(strings.TrimPrefix(name, "./"))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		h := sha256.New()
		if _, err := io.Copy(h, file); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if got := fmt.Sprintf("%x", h.Sum(nil)); got != want {
			t.Errorf("%s: checksum %s, want %s", name, got, want)
		}
		checked++
	}
	t.Logf("%d files compared", checked)
}
