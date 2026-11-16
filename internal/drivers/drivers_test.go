// Copyright (C) 2026 Lukas Koch and Kyro contributors
// SPDX-License-Identifier: GPL-3.0-or-later

package drivers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kyro-pxe/kyro/internal/cab"
)

// write creates a directory tree from path -> content.
func write(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for path, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

var netkvm = map[string]string{
	"netkvm.inf":           "[Version]\r\n",
	"netkvm.sys":           "driver code",
	"netkvm.cat":           "signature",
	"x64/Co Installer.dll": "more code",
}

func TestAddListRemove(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "drivers"))
	if packs, err := s.List(); err != nil || len(packs) != 0 {
		t.Fatalf("empty store: %v, %v", packs, err)
	}
	if files, size, err := s.Cabinet(); err != nil || files != nil || size != 0 {
		t.Fatalf("cabinet of an empty store: %v, %d, %v", files, size, err)
	}

	src := write(t, netkvm)
	p, err := s.Add(src, "netkvm", false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "netkvm" || len(p.Files) != 4 || p.Size() != int64(len("[Version]\r\n")+len("driver code")+len("signature")+len("more code")) {
		t.Fatalf("pack: %+v", p)
	}
	if infs := p.INFs(); len(infs) != 1 || infs[0] != "netkvm.inf" {
		t.Errorf("INFs = %v", infs)
	}
	if p.Source != src {
		t.Errorf("Source = %q", p.Source)
	}
	// Not readable by everyone, and nothing executable.
	for _, path := range []string{"netkvm", "netkvm/files", "netkvm/files/x64"} {
		if info, err := os.Stat(filepath.Join(s.dir, path)); err != nil || info.Mode().Perm() != 0o750 {
			t.Errorf("%s: %v, %v", path, info.Mode(), err)
		}
	}
	for _, path := range []string{"netkvm/pack.json", "netkvm/files/netkvm.sys", "netkvm/files/x64/Co Installer.dll"} {
		if info, err := os.Stat(filepath.Join(s.dir, path)); err != nil || info.Mode().Perm() != 0o640 {
			t.Errorf("%s: %v, %v", path, info.Mode(), err)
		}
	}

	if _, err := s.Add(src, "netkvm", false); !errors.Is(err, ErrExists) {
		t.Errorf("adding twice: %v", err)
	}
	second := write(t, map[string]string{"Storage.INF": "x", "storage.sys": "yy"})
	if _, err := s.Add(second, "vmd", false); err != nil {
		t.Fatal(err)
	}
	packs, err := s.List()
	if err != nil || len(packs) != 2 || packs[0].Name != "netkvm" || packs[1].Name != "vmd" {
		t.Fatalf("List = %+v, %v", packs, err)
	}
	if infs := packs[1].INFs(); len(infs) != 1 || infs[0] != "Storage.INF" {
		t.Errorf("an .INF in upper case is not found: %v", infs)
	}

	// Replacing swaps the whole pack.
	replaced, err := s.Add(second, "netkvm", true)
	if err != nil || len(replaced.Files) != 2 {
		t.Fatalf("replace: %+v, %v", replaced, err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "netkvm", "files", "netkvm.sys")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a file of the replaced pack is still there: %v", err)
	}
	if entries, _ := os.ReadDir(s.dir); len(entries) != 2 {
		t.Errorf("leftovers in the store: %v", entries)
	}

	if err := s.Remove("vmd"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("vmd"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing twice: %v", err)
	}
	if err := s.Remove("../netkvm"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing by a path: %v", err)
	}
	if packs, _ := s.List(); len(packs) != 1 {
		t.Errorf("after Remove: %+v", packs)
	}
}

func TestCabinet(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "drivers"))
	if _, err := s.Add(write(t, netkvm), "netkvm", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(write(t, map[string]string{"vioscsi.inf": "inf", "vioscsi.sys": "sys"}), "vioscsi", false); err != nil {
		t.Fatal(err)
	}
	files, size, err := s.Cabinet()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, f.Name)
	}
	want := `netkvm\netkvm.cat netkvm\netkvm.inf netkvm\netkvm.sys netkvm\x64\Co Installer.dll vioscsi\vioscsi.inf vioscsi\vioscsi.sys`
	if got := strings.Join(names, " "); got != want {
		t.Errorf("files:\n%s\nwant:\n%s", got, want)
	}
	var buf bytes.Buffer
	if err := cab.Write(&buf, files); err != nil {
		t.Fatal(err)
	}
	if int64(buf.Len()) != size {
		t.Errorf("the cabinet has %d bytes, announced were %d", buf.Len(), size)
	}
	if !bytes.Contains(buf.Bytes(), []byte("driver code")) || !bytes.Contains(buf.Bytes(), []byte(`netkvm\x64\Co Installer.dll`)) {
		t.Error("the cabinet lacks a file or its content")
	}
	r, err := files[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(r); string(got) != "signature" {
		t.Errorf("content of %s: %q", files[0].Name, got)
	}
	r.Close()
}

// What is handed to clients must be what was added.
func TestCabinetRefusesChangedPacks(t *testing.T) {
	fresh := func(t *testing.T) *Store {
		s := Open(filepath.Join(t.TempDir(), "drivers"))
		if _, err := s.Add(write(t, netkvm), "netkvm", false); err != nil {
			t.Fatal(err)
		}
		return s
	}
	meta := func(s *Store, change func(*Pack)) {
		path := filepath.Join(s.dir, "netkvm", "pack.json")
		data, _ := os.ReadFile(path)
		var p Pack
		json.Unmarshal(data, &p)
		change(&p)
		data, _ = json.Marshal(p)
		os.WriteFile(path, data, 0o640)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(outside, []byte("not a driver"), 0o600)

	tests := []struct {
		name   string
		change func(s *Store)
	}{
		{"a file has another size", func(s *Store) {
			os.WriteFile(filepath.Join(s.filesDir("netkvm"), "netkvm.sys"), []byte("other"), 0o640)
		}},
		{"a file is gone", func(s *Store) { os.Remove(filepath.Join(s.filesDir("netkvm"), "netkvm.cat")) }},
		{"a file became a link", func(s *Store) {
			path := filepath.Join(s.filesDir("netkvm"), "netkvm.cat")
			os.Remove(path)
			os.Symlink(outside, path)
		}},
		{"the metadata points outside", func(s *Store) {
			meta(s, func(p *Pack) { p.Files[0].Path = "../../../" + filepath.Base(outside) })
		}},
		{"the metadata has an absolute path", func(s *Store) { meta(s, func(p *Pack) { p.Files[0].Path = outside }) }},
		{"the metadata has a backslash", func(s *Store) { meta(s, func(p *Pack) { p.Files[0].Path = `..\x` }) }},
		{"the metadata is for another pack", func(s *Store) { meta(s, func(p *Pack) { p.Name = "other" }) }},
		{"the metadata is damaged", func(s *Store) {
			os.WriteFile(filepath.Join(s.dir, "netkvm", "pack.json"), []byte("{"), 0o640)
		}},
	}
	for _, tt := range tests {
		s := fresh(t)
		tt.change(s)
		if files, _, err := s.Cabinet(); err == nil {
			t.Errorf("%s: Cabinet handed out %d files", tt.name, len(files))
		}
	}
}

func TestAddRejects(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "drivers"))
	good := write(t, netkvm)
	file := filepath.Join(good, "netkvm.inf")
	withLink := write(t, netkvm)
	os.Symlink("/etc/passwd", filepath.Join(withLink, "link.sys"))

	tests := []struct {
		name, src, pack, want string
	}{
		{"name in upper case", good, "NetKVM", "invalid name"},
		{"name with a slash", good, "a/b", "invalid name"},
		{"name too long", good, strings.Repeat("a", 33), "invalid name"},
		{"empty name", good, "", "invalid name"},
		{"missing directory", filepath.Join(good, "missing"), "x", "no such file"},
		{"a file", file, "x", "not a directory"},
		{"no .inf", write(t, map[string]string{"setup.exe": "x"}), "x", "no .inf file"},
		{"empty directory", t.TempDir(), "x", "no .inf file"},
		{"a link", withLink, "x", "not a regular file"},
		{"a name Windows cannot have", write(t, map[string]string{"a.inf": "x", "what?.sys": "x"}), "x", "printable ASCII"},
		{"not ASCII", write(t, map[string]string{"a.inf": "x", "treíber.sys": "x"}), "x", "printable ASCII"},
		{"case only", write(t, map[string]string{"a.inf": "x", "A.INF": "y"}), "x", "upper and lower case"},
		{"path too long", write(t, map[string]string{"a.inf": "x", strings.Repeat("d/", 95) + "a.sys": "x"}), "x", "longer than"},
	}
	for _, tt := range tests {
		if _, err := s.Add(tt.src, tt.pack, false); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want an error with %q", tt.name, err, tt.want)
		}
	}
	if entries, _ := os.ReadDir(s.dir); len(entries) != 0 {
		t.Errorf("refused packs left something behind: %v", entries)
	}
}

func TestLimits(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "drivers"))
	big := write(t, map[string]string{"a.inf": "x"})
	// A sparse file: large without taking the space.
	f, err := os.Create(filepath.Join(big, "big.sys"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxTotal/2 + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if _, err := s.Add(big, "one", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(big, "two", false); err == nil || !strings.Contains(err.Error(), "the limit is 100 MB") {
		t.Errorf("a second pack beyond the limit: %v", err)
	}
	// Replacing a pack counts the new one only.
	if _, err := s.Add(big, "one", true); err != nil {
		t.Errorf("replacing within the limit: %v", err)
	}
	if packs, _ := s.List(); len(packs) != 1 {
		t.Errorf("packs: %+v", packs)
	}

	many := map[string]string{"a.inf": "x"}
	for i := 0; i < MaxFiles; i++ {
		many["f"+strings.Repeat("x", i%7)+"/"+string(rune('a'+i%26))+strings.Repeat("y", i/26)+".sys"] = ""
	}
	if len(many) <= MaxFiles {
		t.Fatalf("the test made only %d files", len(many))
	}
	other := Open(filepath.Join(t.TempDir(), "drivers"))
	if _, err := other.Add(write(t, many), "many", false); err == nil || !strings.Contains(err.Error(), "files; the limit is") {
		t.Errorf("too many files: %v", err)
	}
}
