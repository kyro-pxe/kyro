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

package bootfiles

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func archive(t *testing.T, members map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, data := range members {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(data)
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

// fixture is a pretend release with its pins and a server that offers it.
type fixture struct {
	files   []File
	content map[string][]byte // what the server sends, by path
	server  *httptest.Server
	hits    int
}

func newFixture(t *testing.T) *fixture {
	fx := &fixture{content: map[string][]byte{}}
	fx.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fx.hits++
		data, ok := fx.content[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	t.Cleanup(fx.server.Close)

	loader := []byte("pretend wimboot")
	efi, kpxe := []byte("pretend ipxe.efi"), []byte("pretend undionly.kpxe")
	tgz := archive(t, map[string][]byte{"ipxeboot/x86_64/ipxe.efi": efi, "ipxeboot/x86_64/undionly.kpxe": kpxe, "ipxeboot/other": []byte("x")})
	fx.content["/wimboot"], fx.content["/ipxeboot.tar.gz"] = loader, tgz

	wb := &Download{Name: "wimboot", URL: fx.server.URL + "/wimboot", Size: int64(len(loader)), SHA256: sum(loader)}
	ip := &Download{Name: "ipxeboot.tar.gz", URL: fx.server.URL + "/ipxeboot.tar.gz", Size: int64(len(tgz)), SHA256: sum(tgz)}
	fx.files = []File{
		{Name: "wimboot", From: wb, SHA256: sum(loader)},
		{Name: "ipxe.efi", From: ip, Member: "ipxeboot/x86_64/ipxe.efi", SHA256: sum(efi)},
		{Name: "undionly.kpxe", From: ip, Member: "ipxeboot/x86_64/undionly.kpxe", SHA256: sum(kpxe)},
	}
	return fx
}

func TestFetch(t *testing.T) {
	fx := newFixture(t)
	dir := filepath.Join(t.TempDir(), "boot")
	if err := Fetch(context.Background(), dir, fx.files, Options{}); err != nil {
		t.Fatal(err)
	}
	if fx.hits != 2 {
		t.Errorf("%d downloads, want 2 (the archive once for both of its files)", fx.hits)
	}
	loaded, err := Load(dir, fx.files)
	if err != nil {
		t.Fatal(err)
	}
	if string(loaded["ipxe.efi"]) != "pretend ipxe.efi" || string(loaded["wimboot"]) != "pretend wimboot" || len(loaded) != 3 {
		t.Errorf("loaded %v", loaded)
	}
	if problems := Verify(dir, fx.files); len(problems) != 0 {
		t.Errorf("Verify: %v", problems)
	}

	// A second run finds everything in place and downloads nothing.
	fx.hits = 0
	if err := Fetch(context.Background(), dir, fx.files, Options{}); err != nil || fx.hits != 0 {
		t.Errorf("second Fetch: %v, %d downloads", err, fx.hits)
	}

	// A file that was changed is noticed and replaced.
	os.WriteFile(filepath.Join(dir, "ipxe.efi"), []byte("tampered"), 0o644)
	if problems := Verify(dir, fx.files); len(problems) != 1 || problems[0].File != "ipxe.efi" {
		t.Errorf("Verify after tampering: %v", problems)
	}
	if _, err := Load(dir, fx.files); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("Load after tampering: %v", err)
	}
	if err := Fetch(context.Background(), dir, fx.files, Options{}); err != nil {
		t.Fatal(err)
	}
	if problems := Verify(dir, fx.files); len(problems) != 0 {
		t.Errorf("Verify after repair: %v", problems)
	}
}

func TestFetchRefusesWhatDoesNotMatch(t *testing.T) {
	tests := []struct {
		name string
		edit func(fx *fixture)
		want string
	}{
		{"other content of the same size", func(fx *fixture) { fx.content["/wimboot"] = []byte("PRETEND WIMBOOT") }, "checksum"},
		{"shorter", func(fx *fixture) { fx.content["/wimboot"] = []byte("pretend") }, "bytes"},
		{"longer", func(fx *fixture) { fx.content["/wimboot"] = append(fx.content["/wimboot"], 'x') }, "more than"},
		{"endless", func(fx *fixture) { fx.content["/wimboot"] = bytes.Repeat([]byte("x"), 1<<20) }, "more than"},
		{"gone", func(fx *fixture) { delete(fx.content, "/wimboot") }, "404"},
		{"archive replaced", func(fx *fixture) {
			fx.content["/ipxeboot.tar.gz"] = bytes.Repeat([]byte("x"), len(fx.content["/ipxeboot.tar.gz"]))
		}, "checksum"},
		{"member pin wrong", func(fx *fixture) { fx.files[1].SHA256 = strings.Repeat("0", 64) }, "ipxe.efi has the checksum"},
		{"member missing", func(fx *fixture) { fx.files[1].Member = "ipxeboot/missing" }, "has no"},
	}
	for _, tt := range tests {
		fx := newFixture(t)
		tt.edit(fx)
		dir := filepath.Join(t.TempDir(), "boot")
		err := Fetch(context.Background(), dir, fx.files, Options{})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want one that mentions %q", tt.name, err, tt.want)
		}
		// The file that did not match was not installed.
		bad := "wimboot"
		if strings.Contains(tt.name, "member") || strings.Contains(tt.name, "archive") {
			bad = "ipxe.efi"
		}
		if _, err := os.Stat(filepath.Join(dir, bad)); err == nil {
			t.Errorf("%s: %s was installed anyway", tt.name, bad)
		}
	}
}

func TestFetchFromDirectory(t *testing.T) {
	fx := newFixture(t)
	from := t.TempDir()
	os.WriteFile(filepath.Join(from, "wimboot"), fx.content["/wimboot"], 0o644)
	os.WriteFile(filepath.Join(from, "ipxeboot.tar.gz"), fx.content["/ipxeboot.tar.gz"], 0o644)
	fx.server.Close() // no network

	dir := filepath.Join(t.TempDir(), "boot")
	var log []string
	err := Fetch(context.Background(), dir, fx.files, Options{FromDir: from, Report: func(f string, a ...any) { log = append(log, f) }})
	if err != nil {
		t.Fatal(err)
	}
	if problems := Verify(dir, fx.files); len(problems) != 0 || len(log) == 0 {
		t.Errorf("Verify: %v, %d report lines", problems, len(log))
	}

	// The same checks apply to files from a directory.
	os.WriteFile(filepath.Join(from, "wimboot"), []byte("something else."), 0o644)
	if err := Fetch(context.Background(), filepath.Join(t.TempDir(), "boot"), fx.files, Options{FromDir: from}); err == nil {
		t.Error("a wrong file from a directory was accepted")
	}
	if err := Fetch(context.Background(), filepath.Join(t.TempDir(), "boot"), fx.files, Options{FromDir: t.TempDir()}); err == nil {
		t.Error("an empty directory was accepted")
	}
}

func TestVerifyMissing(t *testing.T) {
	problems := Verify(t.TempDir(), Files)
	if len(problems) != len(Files) {
		t.Fatalf("problems = %v", problems)
	}
	for _, p := range problems {
		if p.Err != ErrMissing {
			t.Errorf("%s: %v", p.File, p.Err)
		}
	}
	if _, err := Load(t.TempDir(), Files); err == nil || !strings.Contains(err.Error(), "kyro bootfiles fetch") {
		t.Errorf("Load: %v", err)
	}
}

// TestPins checks that the compiled-in pins are well-formed. Whether they
// are the right ones is checked by fetching the real files, which a unit
// test must not do.
func TestPins(t *testing.T) {
	for _, f := range Files {
		if len(f.SHA256) != 64 || len(f.From.SHA256) != 64 || f.From.Size <= 0 || !strings.HasPrefix(f.From.URL, "https://github.com/ipxe/") {
			t.Errorf("pin of %s is malformed: %+v from %+v", f.Name, f, f.From)
		}
		if strings.Contains(f.From.URL, "/latest/") || strings.Contains(f.From.URL, "/master/") || strings.Contains(f.From.URL, "/main/") {
			t.Errorf("%s is not pinned to a release: %s", f.Name, f.From.URL)
		}
	}
}

// TestRealFiles fetches the pinned files from a directory named by
// KYRO_TEST_BOOTFILES, which must hold wimboot and ipxeboot.tar.gz.
func TestRealFiles(t *testing.T) {
	from := os.Getenv("KYRO_TEST_BOOTFILES")
	if from == "" {
		t.Skip("KYRO_TEST_BOOTFILES is not set")
	}
	dir := t.TempDir()
	if err := Fetch(context.Background(), dir, Files, Options{FromDir: from}); err != nil {
		t.Fatal(err)
	}
	if problems := Verify(dir, Files); len(problems) != 0 {
		t.Errorf("%v", problems)
	}
}
