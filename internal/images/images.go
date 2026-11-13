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

// Package images keeps the Windows ISOs that Kyro installs from.
//
// An image is an ISO file in the images directory with a small metadata
// file next to it. The ISO is never unpacked or changed. Kyro reads the few
// files it needs straight out of it (package udf): boot.wim for the network
// boot, and the whole tree for the install share.
//
// Kyro does not ship, download or redistribute Windows. The administrator
// supplies the ISO.
package images

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kyro-pxe/kyro/internal/fsutil"
	"github.com/kyro-pxe/kyro/internal/udf"
	"github.com/kyro-pxe/kyro/internal/wim"
)

// Image is the metadata of a stored ISO.
type Image struct {
	Name string `json:"name"`
	// Size and SHA256 are those of the ISO file.
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	// Linked is true if the images directory holds a symbolic link to the
	// ISO instead of a copy.
	Linked bool `json:"linked,omitempty"`
	// BootWIM and InstallImage are the paths of boot.wim and of
	// install.wim or install.esd inside the ISO, spelled as stored there.
	BootWIM      string `json:"boot_wim"`
	InstallImage string `json:"install_image"`
	// Extras are the paths of ei.cfg and pid.txt, if the ISO has them.
	// Setup started from RAM does not look for them on the share; the
	// start script copies them.
	Extras []string `json:"extras,omitempty"`
	// Editions are the editions in the install image. It is empty if they
	// could not be read.
	Editions []wim.Edition `json:"editions,omitempty"`
	AddedAt  time.Time     `json:"added_at"`
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidName reports whether s can be an image name. An image name is also
// the name of its SMB share and part of URLs.
func ValidName(s string) bool { return nameRE.MatchString(s) }

// Errors that callers tell apart.
var (
	ErrExists   = errors.New("an image of that name exists")
	ErrNotFound = errors.New("no such image")
)

// Lookup finds a file in fsys by a slash-separated path, ignoring case, and
// returns the path as it is stored. Windows media are not consistent about
// case: the same file is EI.CFG on one and ei.cfg on another.
func Lookup(fsys fs.FS, name string) (string, error) {
	found := "."
	for _, elem := range strings.Split(name, "/") {
		entries, err := fs.ReadDir(fsys, found)
		if err != nil {
			return "", err
		}
		match := ""
		for _, e := range entries {
			if e.Name() == elem {
				match = elem
				break
			}
			if match == "" && strings.EqualFold(e.Name(), elem) {
				match = e.Name()
			}
		}
		if match == "" {
			return "", &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		}
		found = path.Join(found, match)
	}
	return found, nil
}

// readerAtFile is what the files of an image must support.
type readerAtFile interface {
	fs.File
	io.ReaderAt
}

// plainPath matches paths made of letters, digits, dots, underscores and
// hyphens. The paths Kyro takes from an ISO are written into a batch file,
// and an ISO is a file that someone else made.
var plainPath = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)

// Inspect checks that fsys is the tree of a Windows installation medium and
// fills in what Kyro needs to know about it.
func Inspect(fsys fs.FS) (im Image, err error) {
	defer func() {
		for _, p := range append([]string{im.BootWIM, im.InstallImage}, im.Extras...) {
			if err == nil && !plainPath.MatchString(p) {
				err = fmt.Errorf("the name %q in the image contains characters that Kyro does not accept there", p)
			}
		}
	}()
	if im.BootWIM, err = Lookup(fsys, "sources/boot.wim"); err != nil {
		return im, errors.New("there is no sources/boot.wim; this is not a Windows installation medium")
	}
	if err := checkWIM(fsys, im.BootWIM); err != nil {
		return im, err
	}
	for _, candidate := range []string{"sources/install.wim", "sources/install.esd"} {
		if im.InstallImage, err = Lookup(fsys, candidate); err == nil {
			break
		}
	}
	if im.InstallImage == "" {
		if _, err := Lookup(fsys, "sources/install.swm"); err == nil {
			return im, errors.New("the install image is split (install.swm), which Kyro does not support")
		}
		return im, errors.New("there is no sources/install.wim or sources/install.esd")
	}
	if err := checkWIM(fsys, im.InstallImage); err != nil {
		return im, err
	}
	for _, extra := range []string{"sources/ei.cfg", "sources/pid.txt"} {
		if p, err := Lookup(fsys, extra); err == nil {
			im.Extras = append(im.Extras, p)
		}
	}

	// The editions are nice to have. An image whose description cannot be
	// read still installs.
	if f, err := fsys.Open(im.InstallImage); err == nil {
		defer f.Close()
		if info, err := f.Stat(); err == nil {
			if r, ok := f.(io.ReaderAt); ok {
				im.Editions, _ = wim.Editions(r, info.Size())
			}
		}
	}
	return im, nil
}

func checkWIM(fsys fs.FS, name string) error {
	f, err := fsys.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	r, ok := f.(readerAtFile)
	if !ok || !wim.IsWIM(r) {
		return fmt.Errorf("%s is not a WIM file", name)
	}
	return nil
}

// Store is the images directory:
//
//	<name>.iso    the ISO, or a symbolic link to it
//	<name>.json   the metadata
type Store struct {
	dir string
}

// Open returns the store in dir.
func Open(dir string) *Store { return &Store{dir: dir} }

func (s *Store) isoPath(name string) string  { return filepath.Join(s.dir, name+".iso") }
func (s *Store) metaPath(name string) string { return filepath.Join(s.dir, name+".json") }

// AddOptions says how Add brings an ISO into the store.
type AddOptions struct {
	// Link makes a symbolic link to the ISO instead of copying it. The ISO
	// must then stay where it is and be readable by the service.
	Link bool
	// Progress, if set, is called with the number of bytes read so far and
	// the total while the ISO is checksummed and copied.
	Progress func(done, total int64)
}

// Add checks the ISO at src and stores it under name.
func (s *Store) Add(src, name string, opts AddOptions) (Image, error) {
	if !ValidName(name) {
		return Image{}, fmt.Errorf("%q cannot be an image name: use 1 to 32 lower-case letters, digits and hyphens, not starting with a hyphen", name)
	}
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return Image{}, err
	}
	if _, err := os.Lstat(s.metaPath(name)); err == nil {
		return Image{}, ErrExists
	}
	src, err := filepath.Abs(src)
	if err != nil {
		return Image{}, err
	}
	in, err := os.Open(src)
	if err != nil {
		return Image{}, err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return Image{}, err
	}
	if !info.Mode().IsRegular() {
		return Image{}, fmt.Errorf("%s is not a regular file", src)
	}

	// Look inside before anything is copied.
	fsys, err := udf.Open(in, info.Size())
	if err != nil {
		if errors.Is(err, udf.ErrNotUDF) {
			return Image{}, fmt.Errorf("%s is not a UDF image; Windows ISOs are", src)
		}
		return Image{}, err
	}
	im, err := Inspect(fsys)
	if err != nil {
		return Image{}, fmt.Errorf("%s: %w", src, err)
	}
	im.Name, im.Size, im.Linked = name, info.Size(), opts.Link
	im.AddedAt = time.Now().UTC().Truncate(time.Second)

	// Checksum, and copy unless linking.
	var out *os.File
	dst := s.isoPath(name)
	tmp := dst + ".part"
	if !opts.Link {
		if out, err = os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640); err != nil {
			return Image{}, err
		}
		defer os.Remove(tmp)
		defer out.Close()
	}
	hash := sha256.New()
	buf := make([]byte, 4<<20)
	var done int64
	for {
		n, rerr := in.ReadAt(buf, done)
		if n > 0 {
			hash.Write(buf[:n])
			if out != nil {
				if _, err := out.Write(buf[:n]); err != nil {
					return Image{}, err
				}
			}
			done += int64(n)
			if opts.Progress != nil {
				opts.Progress(done, info.Size())
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return Image{}, rerr
		}
	}
	if done != info.Size() {
		return Image{}, fmt.Errorf("%s changed while it was read", src)
	}
	im.SHA256 = hex.EncodeToString(hash.Sum(nil))

	if opts.Link {
		if err := os.Symlink(src, dst); err != nil {
			return Image{}, err
		}
	} else {
		if err := out.Sync(); err != nil {
			return Image{}, err
		}
		if err := out.Close(); err != nil {
			return Image{}, err
		}
		fsutil.OwnLike(tmp, s.dir)
		if err := os.Rename(tmp, dst); err != nil {
			return Image{}, err
		}
	}
	meta, err := json.MarshalIndent(im, "", "  ")
	if err != nil {
		return Image{}, err
	}
	// The metadata is written last: an image exists once it is there.
	if err := fsutil.WriteFile(s.metaPath(name), append(meta, '\n'), 0o640); err != nil {
		os.Remove(dst)
		return Image{}, err
	}
	return im, nil
}

// Get returns the metadata of an image.
func (s *Store) Get(name string) (Image, error) {
	if !ValidName(name) {
		return Image{}, ErrNotFound
	}
	data, err := os.ReadFile(s.metaPath(name))
	if errors.Is(err, fs.ErrNotExist) {
		return Image{}, ErrNotFound
	}
	if err != nil {
		return Image{}, err
	}
	var im Image
	if err := json.Unmarshal(data, &im); err != nil {
		return Image{}, fmt.Errorf("image %s: %w", name, err)
	}
	if im.Name != name {
		return Image{}, fmt.Errorf("image %s: its metadata is for %q", name, im.Name)
	}
	return im, nil
}

// List returns all images, sorted by name.
func (s *Store) List() ([]Image, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Image
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !ValidName(name) {
			continue
		}
		im, err := s.Get(name)
		if err != nil {
			return nil, err
		}
		out = append(out, im)
	}
	return out, nil
}

// Remove deletes an image. A linked ISO itself is left alone; only the
// link goes.
func (s *Store) Remove(name string) error {
	if _, err := s.Get(name); err != nil {
		return err
	}
	if err := os.Remove(s.metaPath(name)); err != nil {
		return err
	}
	if err := os.Remove(s.isoPath(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Opened is an image that is open for reading.
type Opened struct {
	Image Image
	// FS is the tree inside the ISO.
	FS   *udf.FS
	file *os.File
}

// Close closes the ISO file.
func (o *Opened) Close() error { return o.file.Close() }

// OpenImage opens the ISO of an image. The ISO must still have the size it
// had when it was added.
func (s *Store) OpenImage(name string) (*Opened, error) {
	im, err := s.Get(name)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(s.isoPath(name))
	if err != nil {
		return nil, fmt.Errorf("image %s: %w", name, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.Size() != im.Size {
		f.Close()
		return nil, fmt.Errorf("image %s: the ISO has %d bytes, it had %d when it was added", name, info.Size(), im.Size)
	}
	fsys, err := udf.Open(f, info.Size())
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("image %s: %w", name, err)
	}
	return &Opened{Image: im, FS: fsys, file: f}, nil
}
