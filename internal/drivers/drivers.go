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

// Package drivers stores driver packages that Windows PE needs to reach
// the network or the disk of a client, and hands them out as one cabinet.
//
// A pack is a directory tree as a hardware vendor ships it: .inf files
// with the .sys, .cat and other files they name. Kyro copies it, does not
// look into it beyond finding the .inf files, and changes nothing in it.
package drivers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kyro-pxe/kyro/internal/cab"
	"github.com/kyro-pxe/kyro/internal/fsutil"
)

// The packs travel to the client in one piece and stay in its memory while
// Windows PE runs: once as the cabinet, once unpacked, and once more for
// every driver that Windows PE loads. The limits keep that far below what
// a machine that can run Windows 11 has.
const (
	// MaxTotal is the size of all packs together.
	MaxTotal = 100 << 20
	// MaxFiles is the number of files in all packs together.
	MaxFiles = 2000
	// maxPath is the longest path of a file inside its pack. Windows PE
	// unpacks to X:\$WinPEDriver$\<pack>\, and Windows paths end at 260
	// characters.
	maxPath = 180
)

// File is one file of a pack.
type File struct {
	// Path is relative to the pack, with slashes.
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Pack is a stored driver package.
type Pack struct {
	Name string `json:"name"`
	// Source is where the pack comes from, as a note: the directory it
	// was copied from, or the address it was fetched from.
	Source string `json:"source,omitempty"`
	// Version and License are set for packs that Kyro fetched from their
	// maker.
	Version string    `json:"version,omitempty"`
	License string    `json:"license,omitempty"`
	AddedAt time.Time `json:"added_at"`
	Files   []File    `json:"files"`
}

// Size is the size of all files of the pack.
func (p Pack) Size() int64 {
	var n int64
	for _, f := range p.Files {
		n += f.Size
	}
	return n
}

// INFs returns the .inf files of the pack: the drivers that Windows PE is
// told to load.
func (p Pack) INFs() []string {
	var out []string
	for _, f := range p.Files {
		if isINF(f.Path) {
			out = append(out, f.Path)
		}
	}
	return out
}

func isINF(path string) bool { return strings.EqualFold(filepath.Ext(path), ".inf") }

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidName reports whether s can be the name of a pack. The name becomes
// a directory on the server and in Windows PE.
func ValidName(s string) bool { return nameRE.MatchString(s) }

// Errors that callers tell apart.
var (
	ErrExists   = errors.New("a driver pack of that name exists")
	ErrNotFound = errors.New("no such driver pack")
)

// Store is a directory of packs: <dir>/<name>/pack.json and the files
// under <dir>/<name>/files.
type Store struct {
	dir string
}

// Open returns the store in dir.
func Open(dir string) *Store { return &Store{dir: dir} }

func (s *Store) filesDir(name string) string { return filepath.Join(s.dir, name, "files") }

// scan lists the regular files under dir and checks that each can be
// unpacked in Windows PE.
func scan(dir string) ([]File, error) {
	var files []File
	seen := make(map[string]string)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file (links and devices are not copied)", path)
		}
		rel = filepath.ToSlash(rel)
		if err := cab.CheckName(strings.ReplaceAll(rel, "/", `\`)); err != nil {
			return err
		}
		if len(rel) > maxPath {
			return fmt.Errorf("%s: the path is longer than %d characters, which Windows PE cannot unpack", rel, maxPath)
		}
		if other, dup := seen[strings.ToLower(rel)]; dup {
			return fmt.Errorf("%s and %s differ only in upper and lower case, which Windows does not tell apart", other, rel)
		}
		seen[strings.ToLower(rel)] = rel
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, File{Path: rel, Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// Add copies the directory src into the store as the pack name. With
// replace it overwrites a pack of the same name.
func (s *Store) Add(src, name string, replace bool) (Pack, error) {
	return s.add(src, name, replace, Pack{})
}

// add is Add with a note of where the pack comes from. Of note, only
// Source, Version and License are used.
func (s *Store) add(src, name string, replace bool, note Pack) (Pack, error) {
	if !ValidName(name) {
		return Pack{}, fmt.Errorf("invalid name %q: use lower-case letters, digits and hyphens, at most 32 characters", name)
	}
	info, err := os.Stat(src)
	if err != nil {
		return Pack{}, err
	}
	if !info.IsDir() {
		return Pack{}, fmt.Errorf("%s is not a directory; unpack the driver first and name the directory with its .inf files", src)
	}
	files, err := scan(src)
	if err != nil {
		return Pack{}, err
	}
	pack := Pack{Name: name, Source: src, AddedAt: time.Now().UTC().Truncate(time.Second), Files: files}
	if abs, err := filepath.Abs(src); err == nil {
		pack.Source = abs
	}
	if note.Source != "" {
		pack.Source, pack.Version, pack.License = note.Source, note.Version, note.License
	}
	if len(pack.INFs()) == 0 {
		return Pack{}, fmt.Errorf("%s has no .inf file, so it is not a driver that Windows can load; if the vendor ships an installer (.exe), unpack it first", src)
	}

	// The limits hold for all packs together.
	others, err := s.List()
	if err != nil {
		return Pack{}, err
	}
	total, count := pack.Size(), len(files)
	for _, o := range others {
		if o.Name == name {
			if !replace {
				return Pack{}, ErrExists
			}
			continue
		}
		total += o.Size()
		count += len(o.Files)
	}
	if total > MaxTotal {
		return Pack{}, fmt.Errorf("with this pack (%d MB) all packs together would have %d MB; the limit is %d MB, because Windows PE holds them in memory. Add only the drivers Windows PE needs for the network card and the disk controller", mb(pack.Size()), mb(total), MaxTotal>>20)
	}
	if count > MaxFiles {
		return Pack{}, fmt.Errorf("with this pack all packs together would have %d files; the limit is %d", count, MaxFiles)
	}

	if err := fsutil.MkdirStore(s.dir); err != nil {
		return Pack{}, err
	}
	// Build the pack next to its final place and move it there in one
	// step, so that a half-copied pack is never handed out.
	tmp, err := os.MkdirTemp(s.dir, ".tmp-")
	if err != nil {
		return Pack{}, err
	}
	defer os.RemoveAll(tmp)
	if err := os.Chmod(tmp, 0o750); err != nil {
		return Pack{}, err
	}
	fsutil.OwnLike(tmp, s.dir)
	if err := fsutil.Mkdir(filepath.Join(tmp, "files"), 0o750); err != nil {
		return Pack{}, err
	}
	for _, f := range files {
		if err := copyFile(filepath.Join(src, filepath.FromSlash(f.Path)), filepath.Join(tmp, "files"), f); err != nil {
			return Pack{}, err
		}
	}
	meta, err := json.MarshalIndent(pack, "", "  ")
	if err != nil {
		return Pack{}, err
	}
	if err := fsutil.WriteFile(filepath.Join(tmp, "pack.json"), append(meta, '\n'), 0o640); err != nil {
		return Pack{}, err
	}

	final := filepath.Join(s.dir, name)
	if replace {
		old := tmp + ".old"
		if err := os.Rename(final, old); err == nil {
			defer os.RemoveAll(old)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return Pack{}, err
		}
	}
	if err := os.Rename(tmp, final); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Pack{}, ErrExists
		}
		return Pack{}, err
	}
	fsutil.SyncDir(s.dir)
	return pack, nil
}

func mb(n int64) int64 { return (n + 1<<20 - 1) >> 20 }

// copyFile copies one file of a pack below root, creating its directories,
// and fails if the file is not as large as it was when it was listed.
func copyFile(src, root string, f File) error {
	// Directories one by one, so that each gets the mode and the owner
	// that lets the service read it.
	dir := root
	parts := strings.Split(f.Path, "/")
	for _, part := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, part)
		if err := fsutil.Mkdir(dir, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	dst := filepath.Join(dir, parts[len(parts)-1])
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, in)
	if err == nil && n != f.Size {
		err = fmt.Errorf("%s changed while it was copied", src)
	}
	if err == nil {
		err = out.Chmod(0o640)
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	fsutil.OwnLike(dst, root)
	return err
}

// Get returns a pack.
func (s *Store) Get(name string) (Pack, error) {
	if !ValidName(name) {
		return Pack{}, ErrNotFound
	}
	data, err := os.ReadFile(filepath.Join(s.dir, name, "pack.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return Pack{}, ErrNotFound
	}
	if err != nil {
		return Pack{}, err
	}
	var p Pack
	if err := json.Unmarshal(data, &p); err != nil {
		return Pack{}, fmt.Errorf("driver pack %s: %w", name, err)
	}
	if p.Name != name {
		return Pack{}, fmt.Errorf("driver pack %s: its metadata is for another pack", name)
	}
	return p, nil
}

// List returns all packs, sorted by name.
func (s *Store) List() ([]Pack, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Pack
	for _, e := range entries {
		if !e.IsDir() || !ValidName(e.Name()) {
			continue
		}
		p, err := s.Get(e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Remove deletes a pack.
func (s *Store) Remove(name string) error {
	if _, err := s.Get(name); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.dir, name))
}

// CabinetName is the name under which the cabinet is put into Windows PE.
// wimboot places it in X:\Windows\System32.
const CabinetName = "kyrodrv.cab"

// Cabinet returns all packs as the files of one cabinet, each pack in a
// directory of its name, and the size of that cabinet. Without packs it
// returns no files.
//
// What is on disk must still be what was added: a file that is missing or
// has another size, or a path that the metadata should never have held, is
// an error, and nothing is handed out.
func (s *Store) Cabinet() ([]cab.File, int64, error) {
	packs, err := s.List()
	if err != nil || len(packs) == 0 {
		return nil, 0, err
	}
	var files []cab.File
	var total int64
	for _, p := range packs {
		for _, f := range p.Files {
			name := p.Name + `\` + strings.ReplaceAll(f.Path, "/", `\`)
			if strings.Contains(f.Path, `\`) || !fs.ValidPath(f.Path) || len(f.Path) > maxPath || cab.CheckName(name) != nil {
				return nil, 0, fmt.Errorf("driver pack %s: its metadata names a file that cannot be part of a pack; add the pack again", p.Name)
			}
			path := filepath.Join(s.filesDir(p.Name), filepath.FromSlash(f.Path))
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Size() != f.Size {
				return nil, 0, fmt.Errorf("driver pack %s: %s was changed after the pack was added; add the pack again", p.Name, f.Path)
			}
			total += f.Size
			files = append(files, cab.File{
				Name: name, Size: f.Size, ModTime: info.ModTime(),
				Open: func() (io.ReadCloser, error) { return os.Open(path) },
			})
		}
	}
	if total > MaxTotal || len(files) > MaxFiles {
		return nil, 0, fmt.Errorf("the driver packs are larger than Kyro hands out (%d MB in %d files)", mb(total), len(files))
	}
	size, err := cab.Size(files)
	if err != nil {
		return nil, 0, err
	}
	return files, size, nil
}
