// Copyright (C) 2026 Lukas Koch and Kyro contributors
// SPDX-License-Identifier: GPL-3.0-or-later

package drivers

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kyro-pxe/kyro/internal/cab"
)

// Every source that Kyro offers must be one it could store.
func TestSources(t *testing.T) {
	seen := map[string]bool{}
	for _, src := range Sources {
		if !ValidName(src.Name) || seen[src.Name] {
			t.Errorf("source name %q is invalid or used twice", src.Name)
		}
		seen[src.Name] = true
		for _, field := range []string{src.Title, src.Version, src.Covers, src.Maker, src.License, src.Page, src.URL} {
			if field == "" {
				t.Errorf("%s: a field is empty: %+v", src.Name, src)
			}
		}
		if !strings.HasPrefix(src.URL, "https://") || !strings.HasPrefix(src.Page, "https://") {
			t.Errorf("%s: not fetched over HTTPS", src.Name)
		}
		if src.Format == "exe-zip" && len(src.SHA256) != 64 {
			t.Errorf("%s: a download that is fetched whole needs a checksum", src.Name)
		}
		var total int64
		infs := 0
		to := map[string]bool{}
		for _, f := range src.Files {
			if len(f.SHA256) != 64 || f.Size <= 0 {
				t.Errorf("%s: %s has no size or checksum", src.Name, f.From)
			}
			if err := cab.CheckName(strings.ReplaceAll(f.To, "/", `\`)); err != nil || to[strings.ToLower(f.To)] {
				t.Errorf("%s: %s cannot be stored: %v", src.Name, f.To, err)
			}
			to[strings.ToLower(f.To)] = true
			total += f.Size
			if isINF(f.To) {
				infs++
			}
		}
		if infs == 0 || total > MaxTotal/4 {
			t.Errorf("%s: %d .inf files, %d bytes", src.Name, infs, total)
		}
		if got, ok := FindSource(src.Name); !ok || got.Name != src.Name {
			t.Errorf("FindSource(%q) = %v", src.Name, ok)
		}
	}
	if _, ok := FindSource("missing"); ok {
		t.Error("FindSource found a source that does not exist")
	}
	for _, src := range Sources {
		for _, other := range src.Conflicts {
			o, ok := FindSource(other)
			if !ok || !strings.Contains(strings.Join(o.Conflicts, " "), src.Name) {
				t.Errorf("%s conflicts with %s, which does not say the same", src.Name, other)
			}
			if src.Recommended && o.Recommended {
				t.Errorf("%s and %s conflict and are both offered by the installer", src.Name, other)
			}
		}
	}
	if src, _ := FindSource("intel-rst"); !src.MustAccept || !src.Recommended {
		t.Error("intel-rst must need its license accepted and be offered by the installer")
	}
}

// installer builds a file like a vendor's setup program: a program with
// ZIP archives somewhere inside it.
func installer(t *testing.T, archives ...map[string]string) []byte {
	t.Helper()
	var out bytes.Buffer
	out.WriteString("MZ this is not really a program\x00\x00PK\x05\x06 a stray end record ")
	for _, files := range archives {
		var z bytes.Buffer
		w := zip.NewWriter(&z)
		for name, content := range files {
			f, err := w.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			f.Write([]byte(content))
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		out.Write(z.Bytes())
		out.WriteString(" padding between the archives ")
	}
	return out.Bytes()
}

func zipSource(data []byte, url string) Source {
	return Source{
		Name: "vendor-storage", Title: "Vendor storage driver", Version: "1.2.3", License: "Vendor License", MustAccept: true,
		URL: url, Format: "exe-zip", Size: int64(len(data)), SHA256: sum(data),
		Files: []SourceFile{
			{From: "st.inf", To: "st.inf", Size: 7, SHA256: sum([]byte("the inf"))},
			{From: "st.sys", To: "x64/st.sys", Size: 10, SHA256: sum([]byte("the driver"))},
		},
	}
}

func TestFetchFromAnInstaller(t *testing.T) {
	// Three archives. The first two hold a st.sys that is not the wanted
	// one: of another size, and of the same size with other content. The
	// one with the pinned checksum is taken, wherever it is.
	data := installer(t,
		map[string]string{"other.inf": "another archive", "st.sys": "a file of this name and another size"},
		map[string]string{"st.sys": "THE DRIVER"},
		map[string]string{"st.inf": "the inf", "st.sys": "the driver", "readme.txt": "not wanted"},
		map[string]string{"st.sys": "the DRIVER"},
	)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Write(data)
	}))
	defer srv.Close()
	src := zipSource(data, srv.URL+"/Setup.exe")
	s := Open(filepath.Join(t.TempDir(), "drivers"))
	ctx := context.Background()

	// Not without the license.
	if _, err := s.Fetch(ctx, src, FetchOptions{}); !errors.Is(err, ErrLicense) {
		t.Fatalf("without accepting the license: %v", err)
	}
	if requests != 0 {
		t.Fatal("something was downloaded before the license was accepted")
	}

	var said []string
	p, err := s.Fetch(ctx, src, FetchOptions{AcceptLicense: true, Report: func(f string, a ...any) { said = append(said, f) }})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "vendor-storage" || p.Version != "1.2.3" || p.License != "Vendor License" || p.Source != src.URL || len(p.Files) != 2 {
		t.Errorf("pack: %+v", p)
	}
	if got, _ := os.ReadFile(filepath.Join(s.filesDir(p.Name), "x64", "st.sys")); string(got) != "the driver" {
		t.Errorf("st.sys = %q", got)
	}
	if len(said) == 0 {
		t.Error("nothing was reported")
	}
	// Fetching again replaces the pack.
	if _, err := s.Fetch(ctx, src, FetchOptions{AcceptLicense: true}); err != nil {
		t.Errorf("fetching twice: %v", err)
	}
	// A pack that cannot be used with this one stands in the way, and is
	// named before anything is downloaded.
	if _, err := s.Add(write(t, netkvm), "other-version", false); err != nil {
		t.Fatal(err)
	}
	clash := src
	clash.Conflicts = []string{"missing", "other-version"}
	before := requests
	if _, err := s.Fetch(ctx, clash, FetchOptions{AcceptLicense: true}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "kyro driver remove other-version") || requests != before {
		t.Errorf("with a conflicting pack: %v", err)
	}

	// From a file, without the network.
	file := filepath.Join(t.TempDir(), "Setup.exe")
	os.WriteFile(file, data, 0o644)
	before = requests
	if _, err := s.Fetch(ctx, src, FetchOptions{AcceptLicense: true, FromFile: file}); err != nil || requests != before {
		t.Errorf("from a file: %v, %d requests", err, requests-before)
	}
}

func TestFetchRefuses(t *testing.T) {
	data := installer(t, map[string]string{"st.inf": "the inf", "st.sys": "the driver"})
	other := installer(t, map[string]string{"st.inf": "the inf", "st.sys": "an attack"})
	ctx := context.Background()
	serve := func(body []byte, status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			w.Write(body)
		}))
	}
	tests := []struct {
		name   string
		body   []byte
		status int
		change func(*Source)
		want   string
	}{
		{"another file of the same length", bytes.Replace(data, []byte("padding"), []byte("PADDING"), 1), 200, nil, "not the pinned"},
		{"another installer", other, 200, nil, "not the pinned"},
		{"a longer file", append(bytes.Clone(data), 'x'), 200, nil, "not the pinned"},
		{"a shorter file", data[:len(data)-1], 200, nil, "not the pinned"},
		{"an error page", []byte("<html>not found</html>"), 404, nil, "404"},
		{"a file is missing from the download", data, 200, func(s *Source) {
			s.Files = append(s.Files, SourceFile{From: "st.cat", To: "st.cat", Size: 3, SHA256: sum([]byte("cat"))})
		}, "does not contain st.cat"},
		{"a file inside has another checksum", data, 200, func(s *Source) { s.Files[1].SHA256 = sum([]byte("THE DRIVER")) }, "st.sys has the checksum"},
		{"an unknown format", data, 200, func(s *Source) { s.Format = "rar" }, "unknown format"},
	}
	for _, tt := range tests {
		srv := serve(tt.body, tt.status)
		src := zipSource(data, srv.URL)
		if tt.change != nil {
			tt.change(&src)
		}
		s := Open(filepath.Join(t.TempDir(), "drivers"))
		if _, err := s.Fetch(ctx, src, FetchOptions{AcceptLicense: true}); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want an error with %q", tt.name, err, tt.want)
		}
		if packs, _ := s.List(); len(packs) != 0 {
			t.Errorf("%s: a pack was stored", tt.name)
		}
		srv.Close()
	}
}

// image returns the ISO fixture of package iso9660, which is made like the
// virtio-win image.
func image(t *testing.T) []byte {
	t.Helper()
	f, err := os.Open("../iso9660/testdata/level4.iso.gz")
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

func isoSource(size int64, url string) Source {
	sys := make([]byte, 5000)
	for i := range sys {
		sys[i] = byte(i % 251)
	}
	return Source{
		Name: "virt", Title: "Virtual drivers", Version: "0.1", License: "BSD 3-Clause",
		URL: url, Format: "iso", Size: size,
		Files: []SourceFile{
			{From: "NetKVM/w11/amd64/netkvm.inf", To: "NetKVM/netkvm.inf", Size: 9, SHA256: sum([]byte("the inf\r\n"))},
			{From: "NetKVM/w11/amd64/netkvm.sys", To: "NetKVM/netkvm.sys", Size: 5000, SHA256: sum(sys)},
			{From: "license.txt", To: "license.txt", Size: 7, SHA256: sum([]byte("license"))},
		},
	}
}

func TestFetchFromAnImage(t *testing.T) {
	data := image(t)
	var sent int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		http.ServeContent(rec, r, "image.iso", time.Time{}, bytes.NewReader(data))
		sent += int64(rec.Body.Len())
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes())
	}))
	defer srv.Close()
	ctx := context.Background()
	s := Open(filepath.Join(t.TempDir(), "drivers"))

	// No license to accept: it works without the statement.
	p, err := s.Fetch(ctx, isoSource(int64(len(data)), srv.URL), FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 3 || len(p.INFs()) != 1 || p.INFs()[0] != "NetKVM/netkvm.inf" {
		t.Errorf("pack: %+v", p)
	}
	// The point of reading in parts: far less than the image came over.
	if sent > int64(len(data))/4 {
		t.Errorf("%d of %d bytes were downloaded", sent, len(data))
	}

	// From a file.
	file := filepath.Join(t.TempDir(), "image.iso")
	os.WriteFile(file, data, 0o644)
	before := sent
	if _, err := s.Fetch(ctx, isoSource(int64(len(data)), srv.URL), FetchOptions{FromFile: file}); err != nil || sent != before {
		t.Errorf("from a file: %v", err)
	}
	if _, err := s.Fetch(ctx, isoSource(int64(len(data))+2048, srv.URL), FetchOptions{FromFile: file}); err == nil || !strings.Contains(err.Error(), "not the pinned") {
		t.Errorf("a file of another size: %v", err)
	}

	// The image on the server is not the pinned one: another length.
	fresh := Open(filepath.Join(t.TempDir(), "drivers"))
	if _, err := fresh.Fetch(ctx, isoSource(int64(len(data))-2048, srv.URL), FetchOptions{}); err == nil || !strings.Contains(err.Error(), "not the pinned one") {
		t.Errorf("an image of another length on the server: %v", err)
	}
	// A file in the image has other content than pinned.
	src := isoSource(int64(len(data)), srv.URL)
	src.Files[2].SHA256 = sum([]byte("LICENSE"))
	if _, err := fresh.Fetch(ctx, src, FetchOptions{}); err == nil || !strings.Contains(err.Error(), "license.txt has the checksum") {
		t.Errorf("a changed file in the image: %v", err)
	}
	// A file is larger in the image than pinned: it is not even read.
	src = isoSource(int64(len(data)), srv.URL)
	src.Files[1].Size = 4999
	if _, err := fresh.Fetch(ctx, src, FetchOptions{}); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("a larger file in the image: %v", err)
	}
	if packs, _ := fresh.List(); len(packs) != 0 {
		t.Errorf("a pack was stored: %+v", packs)
	}

	// A server that ignores the range and sends everything.
	whole := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data) }))
	defer whole.Close()
	if _, err := fresh.Fetch(ctx, isoSource(int64(len(data)), whole.URL), FetchOptions{}); err == nil || !strings.Contains(err.Error(), "where a part of the file was asked for") {
		t.Errorf("a server without ranges: %v", err)
	}
}
