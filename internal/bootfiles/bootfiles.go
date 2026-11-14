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

// Package bootfiles obtains and checks the two third-party programs that
// Kyro boots clients with: iPXE and wimboot.
//
// Both are used as the unmodified binaries that their projects release.
// Kyro does not contain them, does not build them and does not change them.
// It downloads them from the projects' release pages, or takes them from a
// directory, and accepts them only if their SHA-256 checksums are the ones
// compiled into this package.
//
// Both are licensed under the GNU General Public License, version 2. See
// the NOTICE file.
package bootfiles

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/kyro-pxe/kyro/internal/fsutil"
)

// Download is a file on a release page.
type Download struct {
	Name   string // file name, also when it is taken from a directory
	URL    string
	Size   int64
	SHA256 string
}

// File is one of the files Kyro serves, with where it comes from.
type File struct {
	// Name is the name in the boot directory and the name clients ask for.
	Name string
	// From is the download that contains it. Member is its path inside
	// that download if the download is an archive, and empty if the
	// download is the file itself.
	From   *Download
	Member string
	SHA256 string
}

// The pinned releases. The checksums of the downloads are the ones GitHub
// publishes for the release assets; those of the archive members were
// computed from the archive.
var (
	WimbootRelease = &Download{
		Name:   "wimboot",
		URL:    "https://github.com/ipxe/wimboot/releases/download/v2.9.0/wimboot",
		Size:   76064,
		SHA256: "5f067ccdc4d084d5bf77b6c853bd0f8402dfc2b4cd1b103d358993ae97fae8e3",
	}
	IPXERelease = &Download{
		Name:   "ipxeboot.tar.gz",
		URL:    "https://github.com/ipxe/ipxe/releases/download/v2.0.0/ipxeboot.tar.gz",
		Size:   12002760,
		SHA256: "01a526d4cc791fc30362259c609d6c506cc64a7bdff51b9a5eb788354e17eee1",
	}

	// Files are the files of the boot directory.
	Files = []File{
		{Name: "wimboot", From: WimbootRelease, SHA256: WimbootRelease.SHA256},
		{Name: "ipxe.efi", From: IPXERelease, Member: "ipxeboot/x86_64/ipxe.efi", SHA256: "868aa34057ff416ebf2fdfb5781de035e2c540477c04039198a9f8a9c6130034"},
		{Name: "undionly.kpxe", From: IPXERelease, Member: "ipxeboot/x86_64/undionly.kpxe", SHA256: "4186562d21ff54e970d905751c9f36d628e73a51a94afe4a6a42f925b0df448c"},
		// For UEFI Secure Boot. The shim is signed by Microsoft; it starts
		// the iPXE next to it, which the iPXE project signed with a key
		// that this shim trusts. Both are part of the iPXE release and are
		// used as they are. wimboot above is signed by Microsoft as well.
		{Name: ShimName, From: IPXERelease, Member: "ipxeboot/x86_64-sb/shimx64.efi", SHA256: "83ad71c7d4f2cf328b75b653d09bf3bea5f29bee2e67ca058f37d83c07133885"},
		{Name: SignedIPXEName, From: IPXERelease, Member: "ipxeboot/x86_64-sb/ipxe.efi", SHA256: "6558e37887516b246d6a97122e8d18bedfe4197b7ba7f67bf1bf102a16678d33"},
	}
)

// The names of the Secure Boot files in the boot directory.
//
// A shim finds the program it starts by its own name: "ipxe-shim.efi" asks
// the server it came from for "ipxe.efi". Kyro therefore keeps the signed
// iPXE under a name of its own and answers a request for "ipxe.efi" with it
// when Secure Boot is switched on in the configuration.
const (
	ShimName       = "ipxe-shim.efi"
	SignedIPXEName = "ipxe-signed.efi"
)

// Versions describes the pinned releases in words.
const Versions = "iPXE v2.0.0, wimboot v2.9.0"

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// Options says where Fetch takes the downloads from.
type Options struct {
	// FromDir, if set, is a directory that holds the downloads under their
	// names. Nothing is fetched from the network then.
	FromDir string
	// Client is the HTTP client for downloads. Nil means one with a
	// timeout of five minutes.
	Client *http.Client
	// Report, if set, is told what happens.
	Report func(format string, args ...any)
}

// Fetch puts the given files into dir. A file is written only after the
// download it comes from and the file itself have the pinned checksums.
// Files that are already there and correct are left alone.
func Fetch(ctx context.Context, dir string, files []File, opts Options) error {
	report := opts.Report
	if report == nil {
		report = func(string, ...any) {}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	downloads := map[*Download][]byte{}
	for _, f := range files {
		if data, err := os.ReadFile(filepath.Join(dir, f.Name)); err == nil && sum(data) == f.SHA256 {
			report("%s is present and has the pinned checksum", f.Name)
			continue
		}
		raw, ok := downloads[f.From]
		if !ok {
			var err error
			if raw, err = obtain(ctx, f.From, opts, report); err != nil {
				return err
			}
			downloads[f.From] = raw
		}
		data := raw
		if f.Member != "" {
			var err error
			if data, err = member(raw, f.Member); err != nil {
				return fmt.Errorf("%s: %w", f.From.Name, err)
			}
		}
		if got := sum(data); got != f.SHA256 {
			return fmt.Errorf("%s has the checksum %s, not the pinned %s; nothing was installed", f.Name, got, f.SHA256)
		}
		// Readable by everyone: these are public files, and the service
		// runs as its own user.
		if err := fsutil.WriteFile(filepath.Join(dir, f.Name), data, 0o644); err != nil {
			return err
		}
		report("%s installed (%d bytes, sha256 %s)", f.Name, len(data), f.SHA256)
	}
	return nil
}

// obtain returns the content of a download after checking its size and
// checksum.
func obtain(ctx context.Context, d *Download, opts Options, report func(string, ...any)) ([]byte, error) {
	var data []byte
	if opts.FromDir != "" {
		path := filepath.Join(opts.FromDir, d.Name)
		report("reading %s", path)
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		if data, err = io.ReadAll(io.LimitReader(f, d.Size+1)); err != nil {
			return nil, err
		}
	} else {
		report("downloading %s", d.URL)
		client := opts.Client
		if client == nil {
			client = &http.Client{Timeout: 5 * time.Minute}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.URL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "kyro")
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("downloading %s: %w", d.URL, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("downloading %s: the server answered %s", d.URL, resp.Status)
		}
		// One byte more than expected is enough to notice a file that is
		// too long, without reading whatever the server sends.
		if data, err = io.ReadAll(io.LimitReader(resp.Body, d.Size+1)); err != nil {
			return nil, fmt.Errorf("downloading %s: %w", d.URL, err)
		}
	}
	if int64(len(data)) != d.Size {
		return nil, fmt.Errorf("%s has %s bytes, not the pinned %d; nothing was installed", d.Name, sizeText(len(data), d.Size), d.Size)
	}
	if got := sum(data); got != d.SHA256 {
		return nil, fmt.Errorf("%s has the checksum %s, not the pinned %s; nothing was installed", d.Name, got, d.SHA256)
	}
	return data, nil
}

func sizeText(got int, want int64) string {
	if int64(got) > want {
		return "more than " + fmt.Sprint(want)
	}
	return fmt.Sprint(got)
}

// member returns one file out of a gzip-compressed tar archive.
func member(archive []byte, name string) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("the archive has no %s", name)
		}
		if err != nil {
			return nil, err
		}
		if hdr.Name != name || hdr.Typeflag != tar.TypeReg {
			continue
		}
		const limit = 16 << 20
		data, err := io.ReadAll(io.LimitReader(tr, limit+1))
		if err != nil {
			return nil, err
		}
		if len(data) > limit {
			return nil, fmt.Errorf("%s is larger than %d bytes", name, limit)
		}
		return data, nil
	}
}

// Problem says what is wrong with one file of the boot directory.
type Problem struct {
	File string
	Err  error
}

// ErrMissing means that a boot file is not there.
var ErrMissing = errors.New("missing")

// Verify checks the files in dir against the pinned checksums and returns
// what is wrong, if anything.
func Verify(dir string, files []File) []Problem {
	var out []Problem
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, f.Name))
		switch {
		case errors.Is(err, os.ErrNotExist):
			out = append(out, Problem{f.Name, ErrMissing})
		case err != nil:
			out = append(out, Problem{f.Name, err})
		case sum(data) != f.SHA256:
			out = append(out, Problem{f.Name, fmt.Errorf("has the checksum %s, not the pinned %s", sum(data), f.SHA256)})
		}
	}
	return out
}

// Load reads the files of dir into memory after checking them.
func Load(dir string, files []File) (map[string][]byte, error) {
	out := make(map[string][]byte, len(files))
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, f.Name))
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("boot file %s is missing; run 'kyro bootfiles fetch'", filepath.Join(dir, f.Name))
		}
		if err != nil {
			return nil, err
		}
		if got := sum(data); got != f.SHA256 {
			return nil, fmt.Errorf("boot file %s has the checksum %s, not the pinned %s; run 'kyro bootfiles fetch'", filepath.Join(dir, f.Name), got, f.SHA256)
		}
		out[f.Name] = data
	}
	return out, nil
}
