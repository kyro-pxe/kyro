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

package drivers

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kyro-pxe/kyro/internal/iso9660"
)

// Source is a driver pack that Kyro knows where to get: a fixed version
// from its maker's own server. Kyro does not contain or pass on any of
// these files. It downloads them to the machine it runs on, at the request
// of its administrator, and checks them against the checksums below.
type Source struct {
	// Name is the name of the source and of the pack it becomes.
	Name    string
	Title   string
	Version string
	// Covers says which hardware the drivers are for.
	Covers string
	Maker  string
	// License names the terms under which the maker offers the files, and
	// Page is where the maker shows the download and those terms.
	License string
	Page    string
	// MustAccept is set if the maker lets nobody download the files
	// without accepting the license. Kyro then fetches them only for an
	// administrator who says that they accept it.
	MustAccept bool
	// Recommended marks what install.sh offers to fetch.
	Recommended bool
	// Conflicts names sources whose packs cannot be stored next to this
	// one, because both carry a driver of the same name in different
	// versions, and Windows PE can hold one version of a driver.
	Conflicts []string

	// URL is the download, Size its length. A Format of "exe-zip" is a
	// program with ZIP archives inside; it is fetched whole and must have
	// the checksum SHA256. A Format of "iso" is an ISO 9660 image, of
	// which only the sectors that hold the files are fetched; there the
	// checksums of the files are what is checked.
	URL    string
	Format string
	Size   int64
	SHA256 string
	Files  []SourceFile
}

// SourceFile is one file of a source.
type SourceFile struct {
	// From is the path inside the download, To the path inside the pack.
	From, To string
	Size     int64
	SHA256   string
}

// ErrConflict means that a pack is stored that cannot be used together
// with the source.
var ErrConflict = errors.New("a driver pack is stored that cannot be used together with this one")

// ErrLicense means that a source needs the administrator to accept its
// license, and they have not.
var ErrLicense = errors.New("the license of these drivers was not accepted")

// Sources lists what Kyro can fetch.
var Sources = []Source{
	{
		Name:        "intel-rst",
		Title:       "Intel Rapid Storage Technology (VMD) storage driver",
		Version:     "20.4.0.1006",
		Covers:      "Intel VMD and RST storage controllers of 12th to 15th generation Intel platforms. Without it Windows Setup finds no disk on machines whose firmware has VMD switched on.",
		Maker:       "Intel",
		License:     "Intel Software License Agreement",
		Page:        "https://www.intel.com/content/www/us/en/download/930427/",
		MustAccept:  true,
		Recommended: true,
		Conflicts:   []string{"intel-rst-11"},
		URL:         "https://downloadmirror.intel.com/930430/SetupRST.exe",
		Format:      "exe-zip",
		Size:        8518896,
		SHA256:      "5372ac4d13983f80e662e3f76c3b5817ef31e268682bd1cdfdcb8c7d293d5218",
		Files: []SourceFile{
			{From: "iaStorVD.inf", To: "iaStorVD.inf", Size: 29398, SHA256: "80d0ee6e18cc147b538a66363c5cf2cc15dbc4f8a9ac97b1a60463de5772a09d"},
			{From: "iaStorVD.sys", To: "iaStorVD.sys", Size: 1737728, SHA256: "716c81dacf5babf0cb60f6892f46c0c2777ca8a74747335dd74ca890f68209d5"},
			{From: "iaStorVD.cat", To: "iaStorVD.cat", Size: 12547, SHA256: "c66ea13322e93899f87abd0cd3e4303b284a16446c0e2422d8d7ca6fcb8c2dd4"},
			// The .inf names these two; without them Windows refuses the driver.
			{From: "RstMwService.exe", To: "RstMwService.exe", Size: 2068472, SHA256: "cf1d996d71202c0b6d873be34cafb29168f934e992368a8c835e448175a1712d"},
			{From: "RstMwEventLogMsg.dll", To: "RstMwEventLogMsg.dll", Size: 32768, SHA256: "6ded2db5190e5bea17f01cffceddf0acaa0e3bbeef307f7bddf65fe0ad75b438"},
		},
	},
	{
		Name:       "intel-rst-11",
		Title:      "Intel Rapid Storage Technology (VMD) storage driver for 11th generation platforms",
		Version:    "18.7.6.1010",
		Covers:     "Intel VMD storage controllers of 11th generation Intel platforms (Tiger Lake), which the newer driver in intel-rst does not cover. It cannot be stored together with intel-rst.",
		Maker:      "Intel",
		License:    "Intel Software License Agreement",
		Page:       "https://www.intel.com/content/www/us/en/download/19512/",
		MustAccept: true,
		Conflicts:  []string{"intel-rst"},
		URL:        "https://downloadmirror.intel.com/773229/SetupRST.exe",
		Format:     "exe-zip",
		Size:       15118552,
		SHA256:     "a2b2e20d6d8100e9ee344746f80849524c64490b90686a13c09268cadb976b37",
		// The download holds several drivers with files of the same names
		// and other content. These are the ones iaStorVD.inf names.
		Files: []SourceFile{
			{From: "iaStorVD.inf", To: "iaStorVD.inf", Size: 33238, SHA256: "a068d501f3df6d7639aab9d3a97de2cb61539e9c734b57855247ead55d5b02a2"},
			{From: "iaStorVD.sys", To: "iaStorVD.sys", Size: 1548488, SHA256: "852ab50860a3a56fe50941ed7a39530297c20dbef7a2dfafc01358f3118d56b0"},
			{From: "iaStorVD.cat", To: "iaStorVD.cat", Size: 14443, SHA256: "2c9240c9742ab4eff2fefdfcb745c8b63a5de7edff3d87f9473dc7b13a2c531b"},
			{From: "iaStorAfs.sys", To: "iaStorAfs.sys", Size: 75464, SHA256: "2246b170611286939c4fafdd817eaf319993de710c1a1fd9667cf2da5b7730b6"},
			{From: "iaStorAfsService.exe", To: "iaStorAfsService.exe", Size: 3234504, SHA256: "a8f340fc18856b0b499233cf61cee5b68d019e693feeeea0fa913eb51d1909e6"},
			{From: "iaStorAfsNative.exe", To: "iaStorAfsNative.exe", Size: 221384, SHA256: "8fde76ba5624c4c329870d9273e1ca797d745ddf0cc9442f7d3ace274029ac87"},
			{From: "Optane.dll", To: "Optane.dll", Size: 135368, SHA256: "888e71deb13ef094fb4054c2bb54058a159999f0584ea205fcb7a78f99c1085c"},
			{From: "OptaneEventLogMsg.dll", To: "OptaneEventLogMsg.dll", Size: 25288, SHA256: "2d6aed519de56caf11e476ac29e7e17bc609ef4987f4231ffa1db523f7e537ab"},
			{From: "RstMwService.exe", To: "RstMwService.exe", Size: 1999048, SHA256: "e4af76e9cb1a6a1716cae503ddce4ca9b08ebc8126d208e8e38ac9a670438104"},
			{From: "RstMwEventLogMsg.dll", To: "RstMwEventLogMsg.dll", Size: 29384, SHA256: "9f92972c9f50c5a0ea0f147493f0ae2e477e0030d2971f5d60958d448da74107"},
		},
	},
	{
		Name:    "virtio",
		Title:   "virtio network and storage drivers (virtio-win)",
		Version: "0.1.302",
		Covers:  "The virtio network card and the virtio SCSI and block controllers of virtual machines under KVM/QEMU, Proxmox and similar. Without them Windows PE reaches neither the network nor the disk of such a machine.",
		Maker:   "the virtio-win project (Red Hat and others)",
		License: "BSD 3-Clause",
		Page:    "https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/archive-virtio/virtio-win-0.1.302-1/",
		URL:     "https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/archive-virtio/virtio-win-0.1.302-1/virtio-win-0.1.302.iso",
		Format:  "iso",
		Size:    877373440,
		Files: []SourceFile{
			{From: "NetKVM/w11/amd64/netkvm.inf", To: "NetKVM/netkvm.inf", Size: 18828, SHA256: "60f7c0ff6815b6edc7a8acf2b46b83c4605ddfbacbcd74b15c9d9653159a3a45"},
			{From: "NetKVM/w11/amd64/netkvm.sys", To: "NetKVM/netkvm.sys", Size: 198296, SHA256: "1b6f8892d39efe28c1a6b20dcc92b76f7dede567d9628fa8d6cebb62d33daf4c"},
			{From: "NetKVM/w11/amd64/netkvm.cat", To: "NetKVM/netkvm.cat", Size: 12489, SHA256: "b4c5c6c70df75777b1e5a44df8fa67bf0f2a85c016cf93f3113c0b3ab01ebc90"},
			{From: "NetKVM/w11/amd64/netkvmp.exe", To: "NetKVM/netkvmp.exe", Size: 203416, SHA256: "d0188e6abc35693776930a757c377067b3738d049a51ec317dbfe5214f5fef74"},
			{From: "vioscsi/w11/amd64/vioscsi.inf", To: "vioscsi/vioscsi.inf", Size: 3203, SHA256: "847db55c4e39053294c33be9b840be75c083a876e90bbf5067b782b0509832d1"},
			{From: "vioscsi/w11/amd64/vioscsi.sys", To: "vioscsi/vioscsi.sys", Size: 79000, SHA256: "2affcb67ff2167891f86f97154e247c1179182d21ee47248f0e5e17d18ac6fb9"},
			{From: "vioscsi/w11/amd64/vioscsi.cat", To: "vioscsi/vioscsi.cat", Size: 12071, SHA256: "65164f7341886268d2165805ad4563b3bdaeee0adac368e0484ce54f8fe6e8fc"},
			{From: "viostor/w11/amd64/viostor.inf", To: "viostor/viostor.inf", Size: 2997, SHA256: "dcf8444fefe57a5dff74cc81ab45f28ea5f784bb636d698a6aa064fffd6f7982"},
			{From: "viostor/w11/amd64/viostor.sys", To: "viostor/viostor.sys", Size: 66200, SHA256: "8820cc432207d7a8edb6f6097c6af0e42729671b973cd196d37c3d8a791d1749"},
			{From: "viostor/w11/amd64/viostor.cat", To: "viostor/viostor.cat", Size: 12074, SHA256: "0223de68d544a31fa052f9088229b3a83c5ab6054f666e013576d8ffae80f163"},
			{From: "virtio-win_license.txt", To: "virtio-win_license.txt", Size: 1598, SHA256: "accd84cb35eff899d619ff40e6fea8074305cdf0e7191db71a5d9c3a015e3b02"},
		},
	},
}

// FindSource returns the source of a name.
func FindSource(name string) (Source, bool) {
	for _, s := range Sources {
		if s.Name == name {
			return s, true
		}
	}
	return Source{}, false
}

// FetchOptions says how Fetch gets a source.
type FetchOptions struct {
	// AcceptLicense is the administrator's statement that they accept the
	// license of the source. It is needed where Source.MustAccept is set.
	AcceptLicense bool
	// FromFile, if set, is a file that holds the download. Nothing is
	// fetched from the network then.
	FromFile string
	// Client is the HTTP client. Nil means one with a timeout of five
	// minutes per request.
	Client *http.Client
	// Report, if set, is told what happens.
	Report func(format string, args ...any)
}

// Fetch downloads a source and stores it as a pack of its name, replacing
// a pack of that name. Nothing is stored unless every file has its pinned
// size and checksum.
func (s *Store) Fetch(ctx context.Context, src Source, opts FetchOptions) (Pack, error) {
	for _, other := range src.Conflicts {
		if _, err := s.Get(other); err == nil {
			return Pack{}, fmt.Errorf("%w: %s and %s both carry a driver of the same name in different versions; remove %s first ('kyro driver remove %s')", ErrConflict, src.Name, other, other, other)
		}
	}
	if src.MustAccept && !opts.AcceptLicense {
		return Pack{}, ErrLicense
	}
	report := opts.Report
	if report == nil {
		report = func(string, ...any) {}
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}

	// What the download holds under each name. A program with several
	// archives inside can hold several files of one name.
	var content map[string][][]byte
	var err error
	switch src.Format {
	case "exe-zip":
		content, err = fetchZips(ctx, src, opts.FromFile, client, report)
	case "iso":
		content, err = fetchISO(ctx, src, opts.FromFile, client, report)
	default:
		err = fmt.Errorf("unknown format %q", src.Format)
	}
	if err != nil {
		return Pack{}, fmt.Errorf("%s: %w", src.Name, err)
	}

	tmp, err := os.MkdirTemp("", "kyro-driver-")
	if err != nil {
		return Pack{}, err
	}
	defer os.RemoveAll(tmp)
	for _, f := range src.Files {
		candidates := content[f.From]
		if len(candidates) == 0 {
			return Pack{}, fmt.Errorf("%s: the download does not contain %s; nothing was stored", src.Name, f.From)
		}
		// The one with the pinned checksum; failing that, any, for the
		// error below.
		data := candidates[0]
		for _, c := range candidates {
			if sum(c) == f.SHA256 {
				data = c
			}
		}
		if int64(len(data)) != f.Size {
			return Pack{}, fmt.Errorf("%s: %s has %d bytes, not the pinned %d; nothing was stored", src.Name, f.From, len(data), f.Size)
		}
		if got := sum(data); got != f.SHA256 {
			return Pack{}, fmt.Errorf("%s: %s has the checksum %s, not the pinned %s; nothing was stored", src.Name, f.From, got, f.SHA256)
		}
		path := filepath.Join(tmp, filepath.FromSlash(f.To))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return Pack{}, err
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return Pack{}, err
		}
	}
	pack, err := s.add(tmp, src.Name, true, Pack{Source: src.URL, Version: src.Version, License: src.License})
	if err != nil {
		return Pack{}, err
	}
	report("%s %s stored as the driver pack %s (%d files)", src.Title, src.Version, pack.Name, len(pack.Files))
	return pack, nil
}

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// wanted returns the files of a source by their path in the download.
func wanted(src Source) map[string]SourceFile {
	out := make(map[string]SourceFile, len(src.Files))
	for _, f := range src.Files {
		out[f.From] = f
	}
	return out
}

// fetchZips gets a download whole, checks it, and takes the files out of
// the ZIP archives that are inside it.
func fetchZips(ctx context.Context, src Source, fromFile string, client *http.Client, report func(string, ...any)) (map[string][][]byte, error) {
	var data []byte
	if fromFile != "" {
		report("reading %s", fromFile)
		f, err := os.Open(fromFile)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		if data, err = io.ReadAll(io.LimitReader(f, src.Size+1)); err != nil {
			return nil, err
		}
	} else {
		report("downloading %s", src.URL)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "kyro")
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("downloading %s: %w", src.URL, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("downloading %s: the server answered %s", src.URL, resp.Status)
		}
		// One byte more than expected is enough to notice a file that is
		// too long, without reading whatever the server sends.
		if data, err = io.ReadAll(io.LimitReader(resp.Body, src.Size+1)); err != nil {
			return nil, fmt.Errorf("downloading %s: %w", src.URL, err)
		}
	}
	if int64(len(data)) != src.Size {
		return nil, fmt.Errorf("the download has %d bytes or more, not the pinned %d; nothing was stored", len(data), src.Size)
	}
	if got := sum(data); got != src.SHA256 {
		return nil, fmt.Errorf("the download has the checksum %s, not the pinned %s; nothing was stored", got, src.SHA256)
	}
	// From here on the bytes are the ones this version of Kyro was written
	// against.
	return zipsInside(data, wanted(src))
}

// zipsInside finds the ZIP archives inside data and returns the wanted
// files from them. An archive is found from its end: the record that
// closes it says how long its directory is and where the directory starts,
// counted from the start of the archive.
func zipsInside(data []byte, want map[string]SourceFile) (map[string][][]byte, error) {
	out := make(map[string][][]byte)
	end := []byte("PK\x05\x06")
	for pos := 0; ; {
		i := bytes.Index(data[pos:], end)
		if i < 0 {
			break
		}
		rec := pos + i
		pos = rec + len(end)
		if rec+22 > len(data) {
			break
		}
		dirSize := int(uint32(data[rec+12]) | uint32(data[rec+13])<<8 | uint32(data[rec+14])<<16 | uint32(data[rec+15])<<24)
		dirOffset := int(uint32(data[rec+16]) | uint32(data[rec+17])<<8 | uint32(data[rec+18])<<16 | uint32(data[rec+19])<<24)
		comment := int(data[rec+20]) | int(data[rec+21])<<8
		start, stop := rec-dirSize-dirOffset, rec+22+comment
		if start < 0 || dirSize < 0 || dirOffset < 0 || stop > len(data) {
			continue
		}
		z, err := zip.NewReader(bytes.NewReader(data[start:stop]), int64(stop-start))
		if err != nil {
			continue // the four bytes were something else
		}
		for _, f := range z.File {
			w, ok := want[f.Name]
			if !ok || f.UncompressedSize64 != uint64(w.Size) {
				continue
			}
			r, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, err)
			}
			content, err := io.ReadAll(io.LimitReader(r, w.Size+1))
			r.Close()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, err)
			}
			out[f.Name] = append(out[f.Name], content)
		}
	}
	return out, nil
}

// fetchISO takes the files of a source out of an ISO image, reading only
// the parts of the image that hold them.
func fetchISO(ctx context.Context, src Source, fromFile string, client *http.Client, report func(string, ...any)) (map[string][][]byte, error) {
	var r io.ReaderAt
	if fromFile != "" {
		report("reading %s", fromFile)
		f, err := os.Open(fromFile)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if info.Size() != src.Size {
			return nil, fmt.Errorf("%s has %d bytes, not the pinned %d; nothing was stored", fromFile, info.Size(), src.Size)
		}
		r = f
	} else {
		report("reading %d files out of %s (the image has %d MB; only the parts that hold them are downloaded)", len(src.Files), src.URL, src.Size>>20)
		r = &rangeReader{ctx: ctx, client: client, url: src.URL, size: src.Size}
	}
	im, err := iso9660.Open(r, src.Size)
	if err != nil {
		return nil, err
	}
	out := make(map[string][][]byte)
	for _, f := range src.Files {
		data, err := im.ReadFile(f.From, f.Size)
		if err != nil {
			return nil, err
		}
		out[f.From] = [][]byte{data}
	}
	return out, nil
}

// rangeReader reads parts of a file on a web server.
type rangeReader struct {
	ctx    context.Context
	client *http.Client
	url    string
	size   int64
}

// ReadAt fetches one range. The server must answer with exactly that range
// of a file of the expected size: an answer with the whole file, or with a
// file of another length, is an error.
func (r *rangeReader) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 || off+int64(len(p)) > r.size {
		return 0, io.ErrUnexpectedEOF
	}
	last := off + int64(len(p)) - 1
	req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "kyro")
	req.Header.Set("Range", "bytes="+strconv.FormatInt(off, 10)+"-"+strconv.FormatInt(last, 10))
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("downloading from %s: %w", r.url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("downloading from %s: the server answered %s where a part of the file was asked for", r.url, resp.Status)
	}
	want := fmt.Sprintf("bytes %d-%d/%d", off, last, r.size)
	if got := strings.TrimSpace(resp.Header.Get("Content-Range")); got != want {
		return 0, fmt.Errorf("downloading from %s: the server sent %q, asked for was %q; the file there is not the pinned one", r.url, got, want)
	}
	n, err := io.ReadFull(resp.Body, p)
	if err != nil {
		return n, fmt.Errorf("downloading from %s: %w", r.url, err)
	}
	return n, nil
}
