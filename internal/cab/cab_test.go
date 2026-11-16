// Copyright (C) 2026 Lukas Koch and Kyro contributors
// SPDX-License-Identifier: GPL-3.0-or-later

package cab

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mem(name string, data []byte) File {
	return File{
		Name: name, Size: int64(len(data)), ModTime: time.Date(2026, 11, 17, 12, 30, 44, 0, time.UTC),
		Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil },
	}
}

func random(seed int64, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

// tree is a driver package as they come: files in directories, an empty
// file, one that ends exactly on a block, one that spans several blocks.
func tree() []File {
	return []File{
		mem(`net\netkvm.inf`, []byte("[Version]\r\nSignature=\"$Windows NT$\"\r\n")),
		mem(`net\netkvm.sys`, random(1, 3*blockSize+17)),
		mem(`net\netkvm.cat`, random(2, blockSize-41)),
		mem(`storage\Intel (R) VMD\iaStorVD.inf`, random(3, 5)),
		mem(`storage\Intel (R) VMD\empty.txt`, nil),
		mem(`storage\Intel (R) VMD\x64\iaStorVD.sys`, random(4, 2*blockSize)),
		mem(`readme.txt`, []byte("x")),
	}
}

func build(t *testing.T, files []File) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := Write(&buf, files); err != nil {
		t.Fatal(err)
	}
	want, err := Size(files)
	if err != nil || want != int64(buf.Len()) {
		t.Fatalf("Size = %d, %v; Write produced %d bytes", want, err, buf.Len())
	}
	return buf.Bytes()
}

// parse reads a cabinet back the way the format describes it, checks every
// offset, size and checksum, and returns the files.
func parse(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	le := binary.LittleEndian
	if string(b[:4]) != "MSCF" || le.Uint32(b[8:]) != uint32(len(b)) || b[24] != 3 || b[25] != 1 {
		t.Fatalf("header: % x", b[:36])
	}
	if le.Uint32(b[4:]) != 0 || le.Uint32(b[12:]) != 0 || le.Uint32(b[20:]) != 0 || le.Uint16(b[30:]) != 0 {
		t.Fatalf("reserved fields or flags are set: % x", b[:36])
	}
	firstFile, folders, files := int(le.Uint32(b[16:])), int(le.Uint16(b[26:])), int(le.Uint16(b[28:]))
	if folders != 1 || firstFile != 36+8 {
		t.Fatalf("%d folders, files at %d", folders, firstFile)
	}
	firstData, blocks, compression := int(le.Uint32(b[36:])), int(le.Uint16(b[40:])), le.Uint16(b[42:])
	if compression != 0 {
		t.Fatalf("compression %d", compression)
	}

	type entry struct {
		name         string
		size, offset int
	}
	var entries []entry
	pos := firstFile
	for i := 0; i < files; i++ {
		end := pos + 16 + bytes.IndexByte(b[pos+16:], 0)
		if le.Uint16(b[pos+8:]) != 0 || le.Uint16(b[pos+14:]) != 0x20 {
			t.Fatalf("file %d: folder %d, attributes %#x", i, le.Uint16(b[pos+8:]), le.Uint16(b[pos+14:]))
		}
		// 2026-11-17 12:30:44
		if date, clock := le.Uint16(b[pos+10:]), le.Uint16(b[pos+12:]); date != (46<<9|11<<5|17) || clock != (12<<11|30<<5|22) {
			t.Fatalf("file %d: date %#x, time %#x", i, date, clock)
		}
		entries = append(entries, entry{string(b[pos+16 : end]), int(le.Uint32(b[pos:])), int(le.Uint32(b[pos+4:]))})
		pos = end + 1
	}
	if pos != firstData {
		t.Fatalf("file entries end at %d, data starts at %d", pos, firstData)
	}

	var content []byte
	for i := 0; i < blocks; i++ {
		stored, unpacked := int(le.Uint16(b[pos+4:])), int(le.Uint16(b[pos+6:]))
		if stored != unpacked || stored == 0 || stored > blockSize || (i < blocks-1 && stored != blockSize) {
			t.Fatalf("block %d: %d bytes stored, %d unpacked", i, stored, unpacked)
		}
		data := b[pos+8 : pos+8+stored]
		if got, want := le.Uint32(b[pos:]), referenceChecksum(data, stored, unpacked); got != want {
			t.Fatalf("block %d: checksum %#x, want %#x", i, got, want)
		}
		content = append(content, data...)
		pos += 8 + stored
	}
	if pos != len(b) {
		t.Fatalf("data ends at %d, the cabinet at %d", pos, len(b))
	}

	out := make(map[string][]byte)
	next := 0
	for _, e := range entries {
		if e.offset != next {
			t.Fatalf("%s starts at %d, the file before it ends at %d", e.name, e.offset, next)
		}
		out[e.name] = content[e.offset : e.offset+e.size]
		next = e.offset + e.size
	}
	if next != len(content) {
		t.Fatalf("files cover %d bytes of %d", next, len(content))
	}
	return out
}

// referenceChecksum is the checksum as Microsoft's description of the
// format gives it, in two steps: first the data, then the two size fields
// with the first result as the start value.
func referenceChecksum(data []byte, stored, unpacked int) uint32 {
	compute := func(b []byte, seed uint32) uint32 {
		sum := seed
		for len(b) >= 4 {
			sum ^= uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
			b = b[4:]
		}
		var ul uint32
		switch len(b) {
		case 3:
			ul |= uint32(b[0]) << 16
			ul |= uint32(b[1]) << 8
			ul |= uint32(b[2])
		case 2:
			ul |= uint32(b[0]) << 8
			ul |= uint32(b[1])
		case 1:
			ul |= uint32(b[0])
		}
		return sum ^ ul
	}
	var sizes [4]byte
	binary.LittleEndian.PutUint16(sizes[0:], uint16(stored))
	binary.LittleEndian.PutUint16(sizes[2:], uint16(unpacked))
	return compute(sizes[:], compute(data, 0))
}

func TestWrite(t *testing.T) {
	files := tree()
	got := parse(t, build(t, files))
	if len(got) != len(files) {
		t.Fatalf("%d files in the cabinet, want %d", len(got), len(files))
	}
	for _, f := range files {
		r, _ := f.Open()
		want, _ := io.ReadAll(r)
		if !bytes.Equal(got[f.Name], want) {
			t.Errorf("%s: content differs (%d bytes, want %d)", f.Name, len(got[f.Name]), len(want))
		}
	}
}

// TestSizes walks the sizes around the edges of a block, where the
// checksum reads its last one to three bytes differently.
func TestSizes(t *testing.T) {
	for _, n := range []int{1, 2, 3, 4, 5, 6, 7, blockSize - 1, blockSize, blockSize + 1, blockSize + 2, blockSize + 3, 2*blockSize + 3} {
		data := random(int64(n), n)
		got := parse(t, build(t, []File{mem("a.bin", data)}))
		if !bytes.Equal(got["a.bin"], data) {
			t.Errorf("%d bytes: content differs", n)
		}
	}
	// Only empty files: a folder without a data block.
	if got := parse(t, build(t, []File{mem("a", nil), mem("b", nil)})); len(got) != 2 {
		t.Errorf("empty files: %v", got)
	}
}

// TestKnownCabinet pins the bytes of a small cabinet. The same bytes were
// unpacked and their checksums verified with cabextract and 7-Zip when
// this test was written.
func TestKnownCabinet(t *testing.T) {
	b := build(t, []File{mem(`dir\a.txt`, []byte("hello\r\n")), mem(`b.bin`, []byte{1, 2, 3, 4, 5})})
	sum := sha256.Sum256(b)
	const want = "6c87a66e5a1c97c0a7ac1ec565f3fd8e353dae192aa492b21e5fad1866621332"
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Errorf("the cabinet changed: sha256 %s, want %s\n% x", got, want, b)
	}
}

func TestRejects(t *testing.T) {
	ok := mem("a", []byte("x"))
	tests := []struct {
		name  string
		files []File
		want  string
	}{
		{"no files", nil, "at least one file"},
		{"empty name", []File{mem("", nil)}, "empty"},
		{"long name", []File{mem(strings.Repeat("a", 256), nil)}, "longer than"},
		{"slash", []File{mem("a/b", nil)}, "printable ASCII"},
		{"colon", []File{mem(`C:\a`, nil)}, "printable ASCII"},
		{"control character", []File{mem("a\nb", nil)}, "printable ASCII"},
		{"not ASCII", []File{mem("tre\u00edber.inf", nil)}, "printable ASCII"},
		{"parent directory", []File{mem(`..\a`, nil)}, "cannot be the name"},
		{"leading backslash", []File{mem(`\a`, nil)}, "cannot be the name"},
		{"trailing backslash", []File{mem(`a\`, nil)}, "cannot be the name"},
		{"trailing dot", []File{mem(`a.\b`, nil)}, "cannot be the name"},
		{"trailing space", []File{mem(`a \b`, nil)}, "cannot be the name"},
		{"twice", []File{ok, mem("A", nil)}, "twice"},
		{"too large", []File{{Name: "a", Size: 1 << 31}}, "do not fit"},
		{"negative size", []File{{Name: "a", Size: -1}}, "do not fit"},
		{"too large together", []File{{Name: "a", Size: 1<<31 - 1}, {Name: "b", Size: 1<<31 - 1}}, "do not fit"},
	}
	for _, tt := range tests {
		if err := Write(io.Discard, tt.files); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want an error with %q", tt.name, err, tt.want)
		}
		if _, err := Size(tt.files); err == nil {
			t.Errorf("%s: Size accepted it", tt.name)
		}
	}
}

// A file that is shorter or longer than announced must not lead to a
// cabinet that looks complete.
func TestChangedFile(t *testing.T) {
	for _, actual := range []int{0, 5, 15, blockSize + 20} {
		f := mem("a", random(1, actual))
		f.Size = 10
		if actual == 10 {
			continue
		}
		err := Write(io.Discard, []File{f, mem("b", []byte("after"))})
		if !errors.Is(err, ErrChanged) {
			t.Errorf("%d bytes for an announced 10: %v", actual, err)
		}
	}
	boom := errors.New("boom")
	f := File{Name: "a", Size: 3, Open: func() (io.ReadCloser, error) { return nil, boom }}
	if err := Write(io.Discard, []File{f}); !errors.Is(err, boom) {
		t.Errorf("Open fails: %v", err)
	}
}

// TestOtherPrograms unpacks a cabinet with the programs that are
// installed, and skips where none is.
func TestOtherPrograms(t *testing.T) {
	files := tree()
	dir := t.TempDir()
	path := filepath.Join(dir, "drivers.cab")
	if err := os.WriteFile(path, build(t, files), 0o644); err != nil {
		t.Fatal(err)
	}
	ran := false
	for _, tool := range []struct {
		name string
		args func(out string) []string
	}{
		{"cabextract", func(out string) []string { return []string{"-q", "-d", out, path} }},
		{"7z", func(out string) []string { return []string{"x", "-y", "-bso0", "-bsp0", "-o" + out, path} }},
	} {
		if _, err := exec.LookPath(tool.name); err != nil {
			continue
		}
		ran = true
		out := filepath.Join(dir, tool.name)
		if msg, err := exec.Command(tool.name, tool.args(out)...).CombinedOutput(); err != nil || bytes.Contains(bytes.ToLower(msg), []byte("checksum")) {
			t.Errorf("%s: %v\n%s", tool.name, err, msg)
			continue
		}
		for _, f := range files {
			r, _ := f.Open()
			want, _ := io.ReadAll(r)
			got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(strings.ReplaceAll(f.Name, `\`, "/"))))
			if err != nil || !bytes.Equal(got, want) {
				t.Errorf("%s: %s: %v (%d bytes, want %d)", tool.name, f.Name, err, len(got), len(want))
			}
		}
	}
	if !ran {
		t.Skip("neither cabextract nor 7z is installed")
	}
}
