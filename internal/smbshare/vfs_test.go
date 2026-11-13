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

package smbshare

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"

	"github.com/jfjallid/go-smb/smb"
	"github.com/jfjallid/go-smb/smb/server"
)

// testTree looks like the part of a Windows ISO that matters here.
func testTree() fstest.MapFS {
	return fstest.MapFS{
		"setup.exe":           {Data: []byte("MZ setup")},
		"sources/boot.wim":    {Data: []byte("boot image")},
		"sources/install.wim": {Data: []byte("0123456789abcdef")},
		"sources/EI.CFG":      {Data: []byte("[Channel]\r\nRetail\r\n")},
		"sources/sxs/a.cab":   {Data: []byte("cab")},
		"boot/bcd":            {Data: []byte("bcd")},
		"Empty":               {Mode: fs.ModeDir | 0o755},
	}
}

func newShareFS(fsys fs.FS, maxHandles int) *shareFS {
	return &shareFS{
		fsys:    fsys,
		handles: newHandleTable(maxHandles),
		log:     slog.New(slog.DiscardHandler),
	}
}

func open(v *shareFS, path string) (*handle, uint32) {
	res, status, err := v.Create(context.Background(), nil, server.CreateRequest{
		Path:              path,
		DesiredAccess:     smb.FAccMaskFileReadData | smb.FAccMaskFileReadAttributes,
		CreateDisposition: smb.FileOpen,
	})
	if err != nil {
		panic(err) // Create never returns an error
	}
	if status != smb.StatusOk {
		return nil, status
	}
	return res.Handle.(*handle), status
}

func TestResolve(t *testing.T) {
	v := newShareFS(testTree(), 100)
	tests := []struct {
		path    string
		status  uint32
		smbPath string
	}{
		{"", smb.StatusOk, ""},
		{`\`, smb.StatusOk, ""},
		{"setup.exe", smb.StatusOk, "setup.exe"},
		{`\setup.exe`, smb.StatusOk, "setup.exe"},
		{"SETUP.EXE", smb.StatusOk, "setup.exe"},
		{"sources", smb.StatusOk, "sources"},
		{`sources\`, smb.StatusOk, "sources"},
		{`sources\install.wim`, smb.StatusOk, `sources\install.wim`},
		{`SOURCES\INSTALL.WIM`, smb.StatusOk, `sources\install.wim`},
		{`sources\ei.cfg`, smb.StatusOk, `sources\EI.CFG`},
		{`Sources\SxS\A.CAB`, smb.StatusOk, `sources\sxs\a.cab`},
		{"empty", smb.StatusOk, "Empty"},

		{"missing.txt", smb.StatusObjectNameNotFound, ""},
		{`sources\missing.wim`, smb.StatusObjectNameNotFound, ""},
		{`missing\install.wim`, smb.StatusObjectPathNotFound, ""},
		{`setup.exe\install.wim`, smb.StatusObjectPathNotFound, ""},
		{`sources\install.wim\x`, smb.StatusObjectPathNotFound, ""},

		{"..", smb.StatusObjectNameInvalid, ""},
		{".", smb.StatusObjectNameInvalid, ""},
		{`sources\..\setup.exe`, smb.StatusObjectNameInvalid, ""},
		{`sources\.\install.wim`, smb.StatusObjectNameInvalid, ""},
		{`..\..\etc\passwd`, smb.StatusObjectNameInvalid, ""},
		{"sources/install.wim", smb.StatusObjectNameInvalid, ""},
		{"../setup.exe", smb.StatusObjectNameInvalid, ""},
		{`sources\\install.wim`, smb.StatusObjectNameInvalid, ""},
		{"setup.exe:stream", smb.StatusObjectNameInvalid, ""},
		{"setup.exe::$DATA", smb.StatusObjectNameInvalid, ""},
		{"*.exe", smb.StatusObjectNameInvalid, ""},
		{"setup?.exe", smb.StatusObjectNameInvalid, ""},
		{"setup.exe\x00", smb.StatusObjectNameInvalid, ""},
		{"set\nup.exe", smb.StatusObjectNameInvalid, ""},
		{"setup\xff.exe", smb.StatusObjectNameInvalid, ""},
		{strings.Repeat("a", maxComponentBytes+1), smb.StatusObjectNameInvalid, ""},
		{strings.Repeat(`a\`, maxPathDepth+1), smb.StatusObjectNameInvalid, ""},
		{strings.Repeat("a", maxPathBytes+1), smb.StatusObjectNameInvalid, ""},
	}
	for _, tt := range tests {
		res, status := v.resolve(tt.path)
		if status != tt.status {
			t.Errorf("resolve(%q) status = %#x, want %#x", tt.path, status, tt.status)
			continue
		}
		if status == smb.StatusOk && res.smbPath != tt.smbPath {
			t.Errorf("resolve(%q) path = %q, want %q", tt.path, res.smbPath, tt.smbPath)
		}
	}
}

func TestCreateRefusesEverythingButReading(t *testing.T) {
	v := newShareFS(testTree(), 100)
	ctx := context.Background()
	read := smb.FAccMaskFileReadData

	for _, disp := range []uint32{smb.FileSupersede, smb.FileCreate, smb.FileOverwrite, smb.FileOverwriteIf, 99} {
		for _, path := range []string{"setup.exe", "new.txt"} {
			_, status, _ := v.Create(ctx, nil, server.CreateRequest{Path: path, DesiredAccess: read, CreateDisposition: disp})
			if status != smb.StatusAccessDenied {
				t.Errorf("disposition %d on %q: status %#x, want access denied", disp, path, status)
			}
		}
	}

	for bit := uint32(1); bit != 0; bit <<= 1 {
		_, status, _ := v.Create(ctx, nil, server.CreateRequest{Path: "setup.exe", DesiredAccess: read | bit, CreateDisposition: smb.FileOpen})
		want := smb.StatusOk
		if bit&writeAccess != 0 {
			want = smb.StatusAccessDenied
		}
		if status != want {
			t.Errorf("access bit %#x: status %#x, want %#x", bit, status, want)
		}
	}
	// The bits that are allowed, spelled out, so that a change to
	// writeAccess shows up here.
	const readBits = smb.FAccMaskFileReadData | smb.FAccMaskFileReadEA | smb.FAccMaskFileExecute |
		smb.FAccMaskFileReadAttributes | smb.FAccMaskReadControl | smb.FAccMaskSynchronize |
		smb.FAccMaskMaximumAllowed | smb.FAccMaskGenericExecute | smb.FAccMaskGenericRead
	const named = 0x000101ff | 0x001f0000 | 0x01000000 | 0x02000000 | 0xf0000000
	if (named&^readBits)&^writeAccess != 0 {
		t.Errorf("bits %#x are neither read access nor refused", (named&^readBits)&^writeAccess)
	}

	_, status, _ := v.Create(ctx, nil, server.CreateRequest{Path: "setup.exe", DesiredAccess: read, CreateDisposition: smb.FileOpen, CreateOptions: smb.FileDeleteOnClose})
	if status != smb.StatusAccessDenied {
		t.Errorf("delete on close: status %#x, want access denied", status)
	}
	_, status, _ = v.Create(ctx, nil, server.CreateRequest{Path: "new.txt", DesiredAccess: read, CreateDisposition: smb.FileOpenIf})
	if status != smb.StatusAccessDenied {
		t.Errorf("open-or-create of a missing file: status %#x, want access denied", status)
	}
	res, status, _ := v.Create(ctx, nil, server.CreateRequest{Path: "setup.exe", DesiredAccess: read, CreateDisposition: smb.FileOpenIf})
	if status != smb.StatusOk || res.CreateAction != smb.FileOpened {
		t.Errorf("open-or-create of an existing file: status %#x action %d", status, res.CreateAction)
	}
	_, status, _ = v.Create(ctx, nil, server.CreateRequest{Path: "setup.exe", DesiredAccess: read, CreateDisposition: smb.FileOpen, CreateOptions: smb.FileOpenByFileId})
	if status != smb.StatusNotSupported {
		t.Errorf("open by file id: status %#x, want not supported", status)
	}
	_, status, _ = v.Create(ctx, nil, server.CreateRequest{Path: "setup.exe", DesiredAccess: read, CreateDisposition: smb.FileOpen, CreateOptions: smb.FileDirectoryFile})
	if status != smb.StatusNotADirectory {
		t.Errorf("file opened as directory: status %#x", status)
	}
	_, status, _ = v.Create(ctx, nil, server.CreateRequest{Path: "sources", DesiredAccess: read, CreateDisposition: smb.FileOpen, CreateOptions: smb.FileNonDirectoryFile})
	if status != smb.StatusFileIsADirectory {
		t.Errorf("directory opened as file: status %#x", status)
	}
}

func TestMutatingOperationsFail(t *testing.T) {
	v := newShareFS(testTree(), 100)
	ctx := context.Background()
	h, _ := open(v, "setup.exe")

	if n, status, _ := v.Write(ctx, h, 0, []byte("x")); n != 0 || status != smb.StatusAccessDenied {
		t.Errorf("Write: n=%d status=%#x", n, status)
	}
	for class := 0; class < 256; class++ {
		if status, _ := v.SetFileInfo(ctx, h, byte(class), make([]byte, 64)); status != smb.StatusAccessDenied {
			t.Errorf("SetFileInfo class %d: status %#x", class, status)
		}
	}
	if _, status, _ := v.Ioctl(ctx, h, 0x00090000, nil, 0); status != smb.StatusNotSupported {
		t.Errorf("Ioctl: status %#x", status)
	}
}

func TestRead(t *testing.T) {
	v := newShareFS(testTree(), 100)
	ctx := context.Background()
	h, status := open(v, `sources\INSTALL.WIM`)
	if status != smb.StatusOk {
		t.Fatalf("open: %#x", status)
	}
	info, _ := h.Stat()
	if info.Name != "install.wim" || info.Size != 16 || info.Attributes != attrNormal {
		t.Errorf("Stat = %+v", info)
	}

	tests := []struct {
		offset int64
		size   int
		want   string
		status uint32
	}{
		{0, 16, "0123456789abcdef", smb.StatusOk},
		{0, 4, "0123", smb.StatusOk},
		{10, 100, "abcdef", smb.StatusOk},
		{16, 4, "", smb.StatusOk}, // end of file: zero bytes
		{1 << 40, 4, "", smb.StatusOk},
		{-1, 4, "", smb.StatusInvalidParameter},
	}
	for _, tt := range tests {
		buf := make([]byte, tt.size)
		n, status, err := v.Read(ctx, h, tt.offset, buf)
		if err != nil || status != tt.status || string(buf[:n]) != tt.want {
			t.Errorf("Read(offset %d, size %d) = %q, %#x, %v", tt.offset, tt.size, buf[:n], status, err)
		}
	}

	v.Close(ctx, h)
	if _, status, _ := v.Read(ctx, h, 0, make([]byte, 4)); status != smb.StatusFileClosed {
		t.Errorf("Read after Close: status %#x", status)
	}

	dir, _ := open(v, "sources")
	if _, status, _ := v.Read(ctx, dir, 0, make([]byte, 4)); status != statusInvalidDeviceRequest {
		t.Errorf("Read on a directory: status %#x", status)
	}
}

func names(entries []server.DirEntry) string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return strings.Join(out, " ")
}

func TestQueryDirectory(t *testing.T) {
	v := newShareFS(testTree(), 100)
	ctx := context.Background()
	dir, _ := open(v, "Sources")

	tests := []struct {
		pattern string
		want    string
		status  uint32
	}{
		{"*", ". .. EI.CFG boot.wim install.wim sxs", smb.StatusOk},
		{"", ". .. EI.CFG boot.wim install.wim sxs", smb.StatusOk},
		{`install"*`, "install.wim", smb.StatusOk},
		{"*.wim", "boot.wim install.wim", smb.StatusOk},
		{"ei.cfg", "EI.CFG", smb.StatusOk},
		{"SXS", "sxs", smb.StatusOk},
		{"*.esd", "", smb.StatusNoSuchFile},
		{"missing", "", smb.StatusNoSuchFile},
	}
	for _, tt := range tests {
		entries, status, err := v.QueryDirectory(ctx, dir, tt.pattern, true)
		if err != nil || status != tt.status || names(entries) != tt.want {
			t.Errorf("QueryDirectory(%q) = %q, %#x, %v; want %q, %#x", tt.pattern, names(entries), status, err, tt.want, tt.status)
		}
	}

	entries, _, _ := v.QueryDirectory(ctx, dir, "*", true)
	for _, e := range entries {
		wantDir := e.Name == "." || e.Name == ".." || e.Name == "sxs"
		if (e.Attributes == attrDirectory) != wantDir {
			t.Errorf("%q: attributes %#x", e.Name, e.Attributes)
		}
	}

	file, _ := open(v, "setup.exe")
	if _, status, _ := v.QueryDirectory(ctx, file, "*", true); status != smb.StatusInvalidParameter {
		t.Errorf("QueryDirectory on a file: status %#x", status)
	}
	empty, _ := open(v, "empty")
	if entries, status, _ := v.QueryDirectory(ctx, empty, "*", true); status != smb.StatusOk || names(entries) != ". .." {
		t.Errorf("QueryDirectory on an empty directory = %q, %#x", names(entries), status)
	}
}

func TestHandleLimit(t *testing.T) {
	v := newShareFS(testTree(), 3)
	ctx := context.Background()
	var handles []*handle
	for _, p := range []string{"setup.exe", "sources", `sources\boot.wim`} {
		h, status := open(v, p)
		if status != smb.StatusOk {
			t.Fatalf("open %q: %#x", p, status)
		}
		handles = append(handles, h)
	}
	if _, status := open(v, "setup.exe"); status != statusTooManyOpenedFiles {
		t.Fatalf("open over the limit: status %#x", status)
	}
	// A failed open must not use up a slot.
	if _, status := open(v, "missing"); status != smb.StatusObjectNameNotFound {
		t.Fatalf("open of a missing file: status %#x", status)
	}
	v.Close(ctx, handles[0])
	v.Close(ctx, handles[0]) // closing twice frees one slot, not two
	if _, status := open(v, "setup.exe"); status != smb.StatusOk {
		t.Fatalf("open after a close: status %#x", status)
	}
	if _, status := open(v, "setup.exe"); status != statusTooManyOpenedFiles {
		t.Fatalf("limit not enforced after a double close: status %#x", status)
	}
	if got := v.handles.count(); got != 3 {
		t.Errorf("open handles = %d, want 3", got)
	}
}

// noReadAt hides ReadAt of the files of a file system.
type noReadAt struct{ fs.FS }
type plainFile struct{ fs.File }

func (n noReadAt) Open(name string) (fs.File, error) {
	f, err := n.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return plainFile{f}, nil
}

func TestFileSystemWithoutReadAt(t *testing.T) {
	v := newShareFS(noReadAt{testTree()}, 100)
	if _, status := open(v, "setup.exe"); status != smb.StatusNotSupported {
		t.Errorf("status %#x, want not supported", status)
	}
	if got := v.handles.count(); got != 0 {
		t.Errorf("open handles = %d after a failed open", got)
	}
}

// TestRealDirectory serves a directory on disk that contains things a share
// must not show: symbolic links, inside and out of the tree, and a pipe.
func TestRealDirectory(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Mkdir(filepath.Join(dir, "sources"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "sources", "Install.wim"), []byte("image"), 0o644))
	must(os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "abs-link.txt")))
	must(os.Symlink("../"+filepath.Base(outside)+"/secret.txt", filepath.Join(dir, "rel-link.txt")))
	must(os.Symlink(outside, filepath.Join(dir, "dir-link")))
	must(os.Symlink("sources/Install.wim", filepath.Join(dir, "inside-link.wim")))
	must(os.Symlink("sources", filepath.Join(dir, "inside-dir-link")))
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o644); err != nil {
		t.Skipf("cannot create a named pipe: %v", err)
	}

	root, err := os.OpenRoot(dir)
	must(err)
	defer root.Close()
	v := newShareFS(root.FS(), 100)
	ctx := context.Background()

	h, status := open(v, `SOURCES\install.WIM`)
	if status != smb.StatusOk {
		t.Fatalf("open: %#x", status)
	}
	buf := make([]byte, 16)
	if n, _, _ := v.Read(ctx, h, 0, buf); string(buf[:n]) != "image" {
		t.Errorf("read %q", buf[:n])
	}
	if h.smbPath != `sources\Install.wim` {
		t.Errorf("path = %q", h.smbPath)
	}

	for _, p := range []string{
		"abs-link.txt", "rel-link.txt", "dir-link", `dir-link\secret.txt`,
		"inside-link.wim", "inside-dir-link", `inside-dir-link\Install.wim`,
		"ABS-LINK.TXT", "pipe", "PIPE",
	} {
		if _, status := open(v, p); status != smb.StatusObjectNameNotFound && status != smb.StatusObjectPathNotFound {
			t.Errorf("open(%q): status %#x, want not found", p, status)
		}
	}

	rootDir, _ := open(v, "")
	entries, _, _ := v.QueryDirectory(ctx, rootDir, "*", true)
	if got := names(entries); got != ". .. sources" {
		t.Errorf("listing = %q, want only the directory", got)
	}
}
