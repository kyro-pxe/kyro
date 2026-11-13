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

package images_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kyro-pxe/kyro/internal/images"
	"github.com/kyro-pxe/kyro/internal/images/imagetest"
	"github.com/kyro-pxe/kyro/internal/udf/udftest"
	"github.com/kyro-pxe/kyro/internal/wim"
)

func TestAddCopies(t *testing.T) {
	src := imagetest.Write(t)
	dir := filepath.Join(t.TempDir(), "images")
	s := images.Open(dir)

	var last, total int64
	im, err := s.Add(src, "win11", images.AddOptions{Progress: func(done, all int64) { last, total = done, all }})
	if err != nil {
		t.Fatal(err)
	}
	iso := imagetest.ISO()
	sum := sha256.Sum256(iso)
	if im.Name != "win11" || im.Size != int64(len(iso)) || im.SHA256 != hex.EncodeToString(sum[:]) || im.Linked {
		t.Errorf("image = %+v", im)
	}
	if im.BootWIM != "sources/boot.wim" || im.InstallImage != "sources/install.wim" || strings.Join(im.Extras, " ") != "sources/EI.CFG" {
		t.Errorf("paths = %q %q %q", im.BootWIM, im.InstallImage, im.Extras)
	}
	if !reflect.DeepEqual(im.Editions, imagetest.Editions) {
		t.Errorf("editions = %+v", im.Editions)
	}
	if last != total || total != int64(len(iso)) {
		t.Errorf("progress ended at %d of %d", last, total)
	}

	// The copy is complete and nothing else is left behind.
	copied, err := os.ReadFile(filepath.Join(dir, "win11.iso"))
	if err != nil || !bytes.Equal(copied, iso) {
		t.Errorf("the copy differs from the source: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("files in the store: %v", entries)
	}

	if _, err := s.Add(src, "win11", images.AddOptions{}); !errors.Is(err, images.ErrExists) {
		t.Errorf("second Add: %v", err)
	}
	got, err := s.Get("win11")
	if err != nil || got.SHA256 != im.SHA256 {
		t.Errorf("Get: %+v, %v", got, err)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 {
		t.Errorf("List: %v, %v", list, err)
	}

	// Reading through the store gives the files of the ISO.
	o, err := s.OpenImage("win11")
	if err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(o.FS, im.BootWIM)
	if err != nil || !bytes.Equal(data, imagetest.BootWIM) {
		t.Errorf("boot.wim through the store: %v", err)
	}
	o.Close()

	// An ISO that changed size is not served.
	os.Truncate(filepath.Join(dir, "win11.iso"), int64(len(iso)-2048))
	if _, err := s.OpenImage("win11"); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Errorf("OpenImage of a truncated ISO: %v", err)
	}

	if err := s.Remove("win11"); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("files left after Remove: %v", entries)
	}
	if err := s.Remove("win11"); !errors.Is(err, images.ErrNotFound) {
		t.Errorf("second Remove: %v", err)
	}
}

func TestAddLinks(t *testing.T) {
	src := imagetest.Write(t)
	dir := filepath.Join(t.TempDir(), "images")
	s := images.Open(dir)
	im, err := s.Add(src, "linked", images.AddOptions{Link: true})
	if err != nil {
		t.Fatal(err)
	}
	if !im.Linked || len(im.SHA256) != 64 {
		t.Errorf("image = %+v", im)
	}
	target, err := os.Readlink(filepath.Join(dir, "linked.iso"))
	if err != nil || target != src {
		t.Errorf("link target = %q, %v", target, err)
	}
	o, err := s.OpenImage("linked")
	if err != nil {
		t.Fatal(err)
	}
	o.Close()
	// Removing the image leaves the ISO where it was.
	if err := s.Remove("linked"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("the linked ISO is gone: %v", err)
	}
}

func TestAddRejects(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	without := func(name string) []byte {
		files := imagetest.Files()
		delete(files, name)
		return udftest.Build(files, udftest.Options{})
	}
	with := func(name string, data []byte) []byte {
		files := imagetest.Files()
		files[name] = data
		return udftest.Build(files, udftest.Options{})
	}
	split := imagetest.Files()
	delete(split, "sources/install.wim")
	split["sources/install.swm"] = wim.Fake(nil, 0)

	tests := []struct {
		name, src, want string
	}{
		{"missing file", filepath.Join(dir, "nope.iso"), "no such file"},
		{"a directory", dir, "not a regular file"},
		{"not an image", write("text.iso", bytes.Repeat([]byte("hello "), 200000)), "not a UDF image"},
		{"empty file", write("empty.iso", nil), "not a UDF image"},
		{"no boot.wim", write("noboot.iso", without("sources/boot.wim")), "sources/boot.wim"},
		{"no install image", write("noinstall.iso", without("sources/install.wim")), "install.wim or sources/install.esd"},
		{"split install image", write("split.iso", udftest.Build(split, udftest.Options{})), "split"},
		{"boot.wim is not a WIM", write("badboot.iso", with("sources/boot.wim", []byte("not a wim at all"))), "not a WIM"},
		{"install.wim is not a WIM", write("badinstall.iso", with("sources/install.wim", []byte("ISO 9660"))), "not a WIM"},
	}
	s := images.Open(filepath.Join(dir, "images"))
	for _, tt := range tests {
		_, err := s.Add(tt.src, "x", images.AddOptions{})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want one that mentions %q", tt.name, err, tt.want)
		}
	}
	good := imagetest.Write(t)
	for _, name := range []string{"", "Win11", "win 11", "../win11", "win11.iso", "-win", strings.Repeat("a", 33), "win_11"} {
		if _, err := s.Add(good, name, images.AddOptions{}); err == nil || !strings.Contains(err.Error(), "image name") {
			t.Errorf("name %q: %v", name, err)
		}
		if _, err := s.Get(name); !errors.Is(err, images.ErrNotFound) {
			t.Errorf("Get(%q): %v", name, err)
		}
	}
	// Nothing of the failed attempts stays behind.
	if entries, _ := os.ReadDir(filepath.Join(dir, "images")); len(entries) != 0 {
		t.Errorf("leftovers: %v", entries)
	}
}

func TestInspectVariants(t *testing.T) {
	// An ESD instead of a WIM, names in another case, and a pid.txt.
	fsys := fstest.MapFS{
		"SOURCES/BOOT.WIM":    {Data: wim.Fake(nil, 0)},
		"SOURCES/Install.ESD": {Data: wim.Fake(imagetest.Editions[:1], 0)},
		"SOURCES/ei.cfg":      {Data: []byte("x")},
		"SOURCES/PID.TXT":     {Data: []byte("x")},
	}
	im, err := images.Inspect(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if im.BootWIM != "SOURCES/BOOT.WIM" || im.InstallImage != "SOURCES/Install.ESD" ||
		strings.Join(im.Extras, " ") != "SOURCES/ei.cfg SOURCES/PID.TXT" || len(im.Editions) != 1 {
		t.Errorf("image = %+v", im)
	}

	// An install image whose description cannot be read is still accepted.
	fsys["SOURCES/Install.ESD"] = &fstest.MapFile{Data: append([]byte("MSWIM\x00\x00\x00"), make([]byte, 300)...)}
	im, err = images.Inspect(fsys)
	if err != nil || len(im.Editions) != 0 {
		t.Errorf("unreadable description: %+v, %v", im, err)
	}
}

func TestInspectRefusesOddNames(t *testing.T) {
	// A crafted ISO whose directory is called something that would be a
	// command in a batch file.
	for _, dir := range []string{"sources & calc", "SOURCES%PATH%", "sources^"} {
		fsys := fstest.MapFS{
			dir + "/boot.wim":    {Data: wim.Fake(nil, 0)},
			dir + "/install.wim": {Data: wim.Fake(nil, 0)},
		}
		// Lookup ignores case, not other characters, so these are simply
		// not found.
		if _, err := images.Inspect(fsys); err == nil {
			t.Errorf("%q: accepted", dir)
		}
	}
	fsys := fstest.MapFS{
		"sources/boot.wim":    {Data: wim.Fake(nil, 0)},
		"sources/install.wim": {Data: wim.Fake(nil, 0)},
	}
	if _, err := images.Inspect(fsys); err != nil {
		t.Errorf("plain names refused: %v", err)
	}
}

func TestTree(t *testing.T) {
	src := imagetest.Write(t)
	s := images.Open(filepath.Join(t.TempDir(), "images"))
	tree := s.Tree()
	defer tree.Close()

	if entries, err := fs.ReadDir(tree, "."); err != nil || len(entries) != 0 {
		t.Fatalf("empty store: %v, %v", entries, err)
	}
	if _, err := s.Add(src, "win11", images.AddOptions{}); err != nil {
		t.Fatal(err)
	}
	// The image shows up without the tree being made again.
	want := []string{"win11/setup.exe", "win11/sources/boot.wim", "win11/sources/install.wim", "win11/sources/EI.CFG", "win11/efi/boot/bootx64.efi"}
	if err := fstest.TestFS(tree, want...); err != nil {
		t.Error(err)
	}
	data, err := fs.ReadFile(tree, "win11/sources/boot.wim")
	if err != nil || !bytes.Equal(data, imagetest.BootWIM) {
		t.Errorf("boot.wim through the tree: %v", err)
	}
	info, err := fs.Stat(tree, "win11")
	if err != nil || !info.IsDir() || info.Name() != "win11" {
		t.Errorf("Stat(win11) = %v, %v", info, err)
	}
	o, err := tree.Image("win11")
	if err != nil || o.Image.InstallImage != "sources/install.wim" {
		t.Errorf("Image: %v", err)
	}

	// A second image next to it.
	if _, err := s.Add(src, "other", images.AddOptions{Link: true}); err != nil {
		t.Fatal(err)
	}
	entries, _ := fs.ReadDir(tree, ".")
	if len(entries) != 2 || entries[0].Name() != "other" || entries[1].Name() != "win11" || !entries[0].IsDir() {
		t.Errorf("top directory = %v", entries)
	}

	// A removed image disappears, also for those who knew its path.
	if err := s.Remove("win11"); err != nil {
		t.Fatal(err)
	}
	if _, err := tree.Open("win11/sources/boot.wim"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("file of a removed image: %v", err)
	}
	if _, err := tree.Image("win11"); !errors.Is(err, images.ErrNotFound) {
		t.Errorf("Image of a removed image: %v", err)
	}
	for _, name := range []string{"missing", "missing/x", "../win11", "Other/setup.exe", "other/../other/setup.exe", ""} {
		if _, err := tree.Open(name); err == nil {
			t.Errorf("Open(%q) succeeded", name)
		}
	}
}

func TestLookup(t *testing.T) {
	fsys := fstest.MapFS{
		"sources/EI.CFG":  {Data: []byte("upper")},
		"sources/ei.cfg":  {Data: []byte("lower")},
		"Sources2/a.txt":  {Data: []byte("x")},
		"boot/Fonts/x.tt": {Data: []byte("x")},
	}
	for in, want := range map[string]string{
		"sources/ei.cfg":  "sources/ei.cfg", // the exact name wins
		"sources/EI.CFG":  "sources/EI.CFG",
		"SOURCES/Ei.Cfg":  "sources/EI.CFG", // otherwise the first match
		"sources2/A.TXT":  "Sources2/a.txt",
		"BOOT/fonts/X.TT": "boot/Fonts/x.tt",
	} {
		if got, err := images.Lookup(fsys, in); err != nil || got != want {
			t.Errorf("Lookup(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"missing", "sources/missing", "sources/ei.cfg/x", "../x", ""} {
		if _, err := images.Lookup(fsys, in); err == nil {
			t.Errorf("Lookup(%q) succeeded", in)
		}
	}
}

// TestRealISO adds a real ISO when KYRO_TEST_ISO names one.
func TestRealISO(t *testing.T) {
	iso := os.Getenv("KYRO_TEST_ISO")
	if iso == "" {
		t.Skip("KYRO_TEST_ISO is not set")
	}
	s := images.Open(filepath.Join(t.TempDir(), "images"))
	im, err := s.Add(iso, "real", images.AddOptions{Link: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(im.Editions) == 0 {
		t.Error("no editions read")
	}
	t.Logf("%+v", im)
}
