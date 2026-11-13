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
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"path"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/jfjallid/go-smb/smb"
	"github.com/jfjallid/go-smb/smb/server"
)

// NTSTATUS codes that the library does not name.
const (
	statusInvalidDeviceRequest  uint32 = 0xc0000010
	statusInsufficientResources uint32 = 0xc000009a
	statusUnexpectedIOError     uint32 = 0xc00000e9
	statusTooManyOpenedFiles    uint32 = 0xc000011f
)

// File attributes (MS-FSCC 2.6).
const (
	attrDirectory uint32 = 0x00000010
	attrNormal    uint32 = 0x00000080
)

// Limits on what a client can ask for. An ISO tree is far below all of
// them.
const (
	maxPathBytes      = 1024
	maxPathDepth      = 64
	maxComponentBytes = 255
	maxDirEntries     = 1 << 16
)

// writeAccess is every bit of an access mask that asks for more than
// reading. A CREATE that sets one of them is refused, so no handle with
// write access ever exists.
const writeAccess = smb.FAccMaskFileWriteData |
	smb.FAccMaskFileAppendData |
	smb.FAccMaskFileWriteEA |
	smb.FAccMaskFileDeleteChild |
	smb.FAccMaskFileWriteAttributes |
	smb.FAccMaskDelete |
	smb.FAccMaskWriteDac |
	smb.FAccMaskWriteOwner |
	smb.FAccMaskAccessSystemSecurity |
	smb.FAccMaskGenericAll |
	smb.FAccMaskGenericWrite

// handleTable holds every open handle of a server, across all its shares.
//
// It caps their number: anyone on the boot network can log in, so without a
// cap one client could use up the file descriptors of the process.
//
// It also remembers which session opened a handle, so that the handles of a
// session can be closed when the session ends. The library does that when a
// connection ends, but not when a client logs off (see ADR 0003).
type handleTable struct {
	max int

	mu   sync.Mutex
	open map[*handle]*server.Session
}

func newHandleTable(max int) *handleTable {
	return &handleTable{max: max, open: make(map[*handle]*server.Session)}
}

// add records an open handle. It reports false if the table is full.
func (t *handleTable) add(h *handle, sess *server.Session) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.open) >= t.max {
		return false
	}
	t.open[h] = sess
	return true
}

func (t *handleTable) remove(h *handle) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.open, h)
}

func (t *handleTable) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.open)
}

// closeWhere closes the handles whose session matches and returns how many
// there were.
func (t *handleTable) closeWhere(match func(*server.Session) bool) int {
	t.mu.Lock()
	var found []*handle
	for h, sess := range t.open {
		if match(sess) {
			found = append(found, h)
		}
	}
	t.mu.Unlock()
	for _, h := range found {
		h.close()
	}
	return len(found)
}

// shareFS serves one fs.FS as a read-only share. It implements server.VFS.
//
// It can only read: fs.FS has no operation that changes anything. Names are
// matched without regard to case. Only directories and regular files are
// visible; symbolic links, devices, sockets and pipes are treated as
// absent.
type shareFS struct {
	fsys    fs.FS
	handles *handleTable
	log     *slog.Logger
}

var _ server.VFS = (*shareFS)(nil)

// handle is an open file or directory.
type handle struct {
	smbPath string // share-relative, "\"-separated, in the stored case
	fsPath  string // name inside the fs.FS
	info    fs.FileInfo
	table   *handleTable

	mu     sync.Mutex
	file   fs.File     // nil for a directory, and after Close
	reader io.ReaderAt // nil for a directory, and after Close
	closed bool
}

func (h *handle) Path() server.Path { return h.smbPath }
func (h *handle) IsDir() bool       { return h.info.IsDir() }

// Stat returns the metadata recorded when the handle was opened. The share
// is read-only, so there is nothing to refresh.
func (h *handle) Stat() (server.FileInfo, error) {
	return toFileInfo(baseName(h.smbPath), h.info), nil
}

func (h *handle) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	if h.file != nil {
		h.file.Close()
		h.file, h.reader = nil, nil
	}
	h.table.remove(h)
}

func baseName(smbPath string) string {
	if i := strings.LastIndexByte(smbPath, '\\'); i >= 0 {
		return smbPath[i+1:]
	}
	return smbPath
}

func toFileInfo(name string, info fs.FileInfo) server.FileInfo {
	attrs := attrNormal
	size := info.Size()
	if info.IsDir() {
		attrs = attrDirectory
		size = 0
	}
	t := info.ModTime()
	return server.FileInfo{
		Name:           name,
		Size:           size,
		AllocationSize: (size + 4095) &^ 4095,
		Attributes:     attrs,
		CreationTime:   t,
		LastAccessTime: t,
		LastWriteTime:  t,
		ChangeTime:     t,
	}
}

// servable reports whether an entry of this type is shown to clients.
func servable(mode fs.FileMode) bool {
	return mode.IsDir() || mode.IsRegular()
}

// validComponent reports whether s can be one element of a path on the
// share. It rejects everything Windows rejects in a file name, and with
// that every way of leaving a directory other than by naming a child.
func validComponent(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > maxComponentBytes || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return false
		}
	}
	return true
}

// resolved is a path that exists on the share.
type resolved struct {
	smbPath string
	fsPath  string
	info    fs.FileInfo
}

// resolve finds the file or directory that an SMB path names. It returns
// an NTSTATUS other than StatusOk if there is none.
func (v *shareFS) resolve(smbPath string) (resolved, uint32) {
	if len(smbPath) > maxPathBytes {
		return resolved{}, smb.StatusObjectNameInvalid
	}
	smbPath = strings.Trim(smbPath, `\`)
	if smbPath == "" {
		info, err := fs.Stat(v.fsys, ".")
		if err != nil || !info.IsDir() {
			v.log.Error("share root is not readable", "err", err)
			return resolved{}, statusUnexpectedIOError
		}
		return resolved{fsPath: ".", info: info}, smb.StatusOk
	}
	parts := strings.Split(smbPath, `\`)
	if len(parts) > maxPathDepth {
		return resolved{}, smb.StatusObjectNameInvalid
	}
	for _, p := range parts {
		if !validComponent(p) {
			return resolved{}, smb.StatusObjectNameInvalid
		}
	}

	res := resolved{fsPath: "."}
	stored := make([]string, 0, len(parts))
	for i, part := range parts {
		last := i == len(parts)-1
		name, info, status := v.lookup(res.fsPath, part)
		if status == smb.StatusObjectNameNotFound && !last {
			status = smb.StatusObjectPathNotFound
		}
		if status != smb.StatusOk {
			return resolved{}, status
		}
		if !last && !info.IsDir() {
			return resolved{}, smb.StatusObjectPathNotFound
		}
		stored = append(stored, name)
		res.fsPath = path.Join(res.fsPath, name)
		res.info = info
	}
	res.smbPath = strings.Join(stored, `\`)
	return res, smb.StatusOk
}

// lookup finds the child of dir called name, ignoring case. It returns the
// name as stored.
func (v *shareFS) lookup(dir, name string) (string, fs.FileInfo, uint32) {
	info, err := fs.Lstat(v.fsys, path.Join(dir, name))
	if err == nil {
		if !servable(info.Mode()) {
			return "", nil, smb.StatusObjectNameNotFound
		}
		return name, info, smb.StatusOk
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", nil, v.statusOf(err, "lookup")
	}
	entries, err := fs.ReadDir(v.fsys, dir)
	if err != nil {
		return "", nil, v.statusOf(err, "lookup")
	}
	for _, e := range entries {
		if !strings.EqualFold(e.Name(), name) || !servable(e.Type()) || !validComponent(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || !servable(info.Mode()) {
			continue
		}
		return e.Name(), info, smb.StatusOk
	}
	return "", nil, smb.StatusObjectNameNotFound
}

// statusOf maps an error of the underlying file system to an NTSTATUS.
func (v *shareFS) statusOf(err error, op string) uint32 {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return smb.StatusObjectNameNotFound
	case errors.Is(err, fs.ErrPermission):
		return smb.StatusAccessDenied
	case errors.Is(err, fs.ErrInvalid):
		return smb.StatusObjectNameInvalid
	}
	v.log.Error("file system error", "op", op, "err", err)
	return statusUnexpectedIOError
}

// Create opens an existing file or directory. It never creates anything.
func (v *shareFS) Create(_ context.Context, sess *server.Session, req server.CreateRequest) (server.CreateResult, uint32, error) {
	none := server.CreateResult{}
	switch req.CreateDisposition {
	case smb.FileOpen, smb.FileOpenIf:
	default:
		return none, smb.StatusAccessDenied, nil
	}
	if req.DesiredAccess&writeAccess != 0 || req.CreateOptions&smb.FileDeleteOnClose != 0 {
		return none, smb.StatusAccessDenied, nil
	}
	if req.CreateOptions&smb.FileOpenByFileId != 0 {
		return none, smb.StatusNotSupported, nil
	}

	res, status := v.resolve(req.Path)
	if status == smb.StatusObjectNameNotFound && req.CreateDisposition == smb.FileOpenIf {
		// The file would have to be created.
		status = smb.StatusAccessDenied
	}
	if status != smb.StatusOk {
		return none, status, nil
	}
	isDir := res.info.IsDir()
	if req.CreateOptions&smb.FileDirectoryFile != 0 && !isDir {
		return none, smb.StatusNotADirectory, nil
	}
	if req.CreateOptions&smb.FileNonDirectoryFile != 0 && isDir {
		return none, smb.StatusFileIsADirectory, nil
	}

	h := &handle{smbPath: res.smbPath, fsPath: res.fsPath, info: res.info, table: v.handles}
	if !isDir {
		f, err := v.fsys.Open(res.fsPath)
		if err != nil {
			return none, v.statusOf(err, "open"), nil
		}
		// The file may have been replaced since it was looked up.
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			f.Close()
			return none, smb.StatusObjectNameNotFound, nil
		}
		reader, ok := f.(io.ReaderAt)
		if !ok {
			f.Close()
			v.log.Error("file system does not support reading at an offset", "path", res.fsPath)
			return none, smb.StatusNotSupported, nil
		}
		h.file, h.reader, h.info = f, reader, info
	}
	if !v.handles.add(h, sess) {
		if h.file != nil {
			h.file.Close()
		}
		return none, statusTooManyOpenedFiles, nil
	}
	return server.CreateResult{
		Handle:       h,
		CreateAction: smb.FileOpened,
		Info:         toFileInfo(baseName(res.smbPath), h.info),
	}, smb.StatusOk, nil
}

// Close releases a handle.
func (v *shareFS) Close(_ context.Context, sh server.Handle) error {
	if h, ok := sh.(*handle); ok {
		h.close()
	}
	return nil
}

// Read reads from an open file.
func (v *shareFS) Read(_ context.Context, sh server.Handle, offset int64, buf []byte) (int, uint32, error) {
	h, ok := sh.(*handle)
	if !ok || offset < 0 {
		return 0, smb.StatusInvalidParameter, nil
	}
	if h.IsDir() {
		return 0, statusInvalidDeviceRequest, nil
	}
	h.mu.Lock()
	reader := h.reader
	h.mu.Unlock()
	if reader == nil {
		return 0, smb.StatusFileClosed, nil
	}
	// Zero bytes without an error is the end of the file; the library
	// turns that into STATUS_END_OF_FILE.
	if offset >= h.info.Size() {
		return 0, smb.StatusOk, nil
	}
	n, err := reader.ReadAt(buf, offset)
	if n == 0 && err != nil && !errors.Is(err, io.EOF) {
		v.log.Error("file system error", "op", "read", "path", h.fsPath, "err", err)
		return 0, statusUnexpectedIOError, nil
	}
	return n, smb.StatusOk, nil
}

// Write always fails.
func (v *shareFS) Write(context.Context, server.Handle, int64, []byte) (int, uint32, error) {
	return 0, smb.StatusAccessDenied, nil
}

// Flush has nothing to do.
func (v *shareFS) Flush(context.Context, server.Handle) (uint32, error) {
	return smb.StatusOk, nil
}

// QueryFileInfo leaves the answer to the library, which builds it from
// Handle.Stat.
func (v *shareFS) QueryFileInfo(context.Context, server.Handle, byte) (any, uint32, error) {
	return nil, smb.StatusNotSupported, nil
}

// SetFileInfo always fails. Renaming, deleting, truncating and changing
// timestamps all arrive here.
func (v *shareFS) SetFileInfo(context.Context, server.Handle, byte, []byte) (uint32, error) {
	return smb.StatusAccessDenied, nil
}

// QueryDirectory lists the children of a directory that match pattern.
func (v *shareFS) QueryDirectory(_ context.Context, sh server.Handle, pattern string, _ bool) ([]server.DirEntry, uint32, error) {
	h, ok := sh.(*handle)
	if !ok || !h.IsDir() {
		return nil, smb.StatusInvalidParameter, nil
	}
	entries, err := fs.ReadDir(v.fsys, h.fsPath)
	if err != nil {
		return nil, v.statusOf(err, "readdir"), nil
	}
	if len(entries) > maxDirEntries {
		v.log.Warn("directory listing cut short", "path", h.fsPath, "entries", len(entries))
		entries = entries[:maxDirEntries]
	}

	var out []server.DirEntry
	for _, dot := range []string{".", ".."} {
		if matchPattern(pattern, dot) {
			out = append(out, server.DirEntry{FileInfo: toFileInfo(dot, h.info)})
		}
	}
	for _, e := range entries {
		name := e.Name()
		if !servable(e.Type()) || !validComponent(name) || !matchPattern(pattern, name) {
			continue
		}
		info, err := e.Info()
		if err != nil || !servable(info.Mode()) {
			continue
		}
		out = append(out, server.DirEntry{FileInfo: toFileInfo(name, info)})
	}
	if len(out) == 0 {
		return nil, smb.StatusNoSuchFile, nil
	}
	return out, smb.StatusOk, nil
}

// QueryFSInfo leaves the answer to the library.
func (v *shareFS) QueryFSInfo(context.Context, byte) (any, uint32, error) {
	return nil, smb.StatusNotSupported, nil
}

// QuerySecurity leaves the answer to the library.
func (v *shareFS) QuerySecurity(context.Context, server.Handle, uint32) ([]byte, uint32, error) {
	return nil, smb.StatusNotSupported, nil
}

// Ioctl supports no control codes.
func (v *shareFS) Ioctl(context.Context, server.Handle, uint32, []byte, uint32) ([]byte, uint32, error) {
	return nil, smb.StatusNotSupported, nil
}
