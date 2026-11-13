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

package images

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"time"
)

// Tree presents all images of a store as one file system: a directory per
// image at the top, and below it the tree inside that image's ISO.
//
// It follows the store: an image that is added shows up, one that is
// removed disappears, without anything being restarted. Kyro serves this
// tree as its install share and takes boot.wim from it.
type Tree struct {
	store *Store

	mu   sync.Mutex
	open map[string]*opened
}

// opened is an image that the tree holds open, with what its metadata file
// looked like at that time.
type opened struct {
	*Opened
	metaTime time.Time
}

// Tree returns the file system of all images in the store.
func (s *Store) Tree() *Tree {
	return &Tree{store: s, open: make(map[string]*opened)}
}

// Close closes every ISO the tree holds open.
func (t *Tree) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for name, o := range t.open {
		o.Close()
		delete(t.open, name)
	}
}

// image returns the open image of a name, opening it on first use and again
// when the image was replaced.
func (t *Tree) image(name string) (*Opened, error) {
	if !ValidName(name) {
		return nil, fs.ErrNotExist
	}
	info, err := os.Stat(t.store.metaPath(name))
	t.mu.Lock()
	defer t.mu.Unlock()
	o := t.open[name]
	if err != nil {
		if o != nil {
			o.Close()
			delete(t.open, name)
		}
		return nil, fs.ErrNotExist
	}
	if o != nil && o.metaTime.Equal(info.ModTime()) {
		return o.Opened, nil
	}
	if o != nil {
		o.Close()
		delete(t.open, name)
	}
	op, err := t.store.OpenImage(name)
	if err != nil {
		return nil, err
	}
	t.open[name] = &opened{Opened: op, metaTime: info.ModTime()}
	return op, nil
}

// Image returns the metadata and the tree of one image.
func (t *Tree) Image(name string) (*Opened, error) {
	o, err := t.image(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return o, err
}

// split divides a path of the tree into the image and the path inside it.
func split(name string) (image, rest string) {
	image, rest, found := strings.Cut(name, "/")
	if !found {
		rest = "."
	}
	return image, rest
}

// Open implements fs.FS.
func (t *Tree) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		entries, err := t.ReadDir(".")
		if err != nil {
			return nil, err
		}
		return &rootDir{entries: entries}, nil
	}
	image, rest := split(name)
	o, err := t.image(image)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	f, err := o.FS.Open(rest)
	if err != nil {
		return nil, err
	}
	if rest == "." {
		return renamed{f, image, o.Image.AddedAt}, nil
	}
	return f, nil
}

// Stat implements fs.StatFS.
func (t *Tree) Stat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		return dirInfo{name: "."}, nil
	}
	image, rest := split(name)
	o, err := t.image(image)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	// The directory of an image carries the time the image was added, here
	// and in the listing of the top directory.
	if rest == "." {
		return dirInfo{name: image, modTime: o.Image.AddedAt}, nil
	}
	return o.FS.Stat(rest)
}

// ReadDir implements fs.ReadDirFS.
func (t *Tree) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		list, err := t.store.List()
		if err != nil {
			return nil, err
		}
		out := make([]fs.DirEntry, len(list))
		for i, im := range list {
			out[i] = fs.FileInfoToDirEntry(dirInfo{name: im.Name, modTime: im.AddedAt})
		}
		return out, nil
	}
	image, rest := split(name)
	o, err := t.image(image)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	return o.FS.ReadDir(rest)
}

var (
	_ fs.StatFS    = (*Tree)(nil)
	_ fs.ReadDirFS = (*Tree)(nil)
)

// dirInfo describes a directory that exists only in the tree.
type dirInfo struct {
	name    string
	modTime time.Time
}

func (d dirInfo) Name() string       { return d.name }
func (d dirInfo) Size() int64        { return 0 }
func (d dirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o555 }
func (d dirInfo) ModTime() time.Time { return d.modTime }
func (d dirInfo) IsDir() bool        { return true }
func (d dirInfo) Sys() any           { return nil }

// rootDir is the open top directory.
type rootDir struct {
	entries []fs.DirEntry
	pos     int
}

func (r *rootDir) Stat() (fs.FileInfo, error) { return dirInfo{name: "."}, nil }
func (r *rootDir) Close() error               { return nil }
func (r *rootDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: ".", Err: errors.New("is a directory")}
}

func (r *rootDir) ReadDir(n int) ([]fs.DirEntry, error) {
	rest := r.entries[r.pos:]
	if n > 0 && len(rest) > n {
		rest = rest[:n]
	}
	if n > 0 && len(rest) == 0 {
		return nil, io.EOF
	}
	r.pos += len(rest)
	return rest, nil
}

// renamed is the top directory of an image under the image's name.
type renamed struct {
	fs.File
	name    string
	modTime time.Time
}

func (r renamed) Stat() (fs.FileInfo, error) {
	return dirInfo{name: r.name, modTime: r.modTime}, nil
}

func (r renamed) ReadDir(n int) ([]fs.DirEntry, error) {
	if d, ok := r.File.(fs.ReadDirFile); ok {
		return d.ReadDir(n)
	}
	return nil, &fs.PathError{Op: "readdir", Path: r.name, Err: errors.New("not a directory")}
}
