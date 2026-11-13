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

// Package fsutil has the few file operations that Kyro's stores share.
package fsutil

import (
	"os"
	"path/filepath"
)

// WriteFile writes data to path so that a reader sees the old or the new
// content, never half of one. The file gets the given mode and, if the
// process runs as root, the owner of its directory: the CLI often runs as
// root while the service runs as its own user and has to read what the CLI
// wrote.
func WriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	OwnLike(tmp.Name(), dir)
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	SyncDir(dir)
	return nil
}

// SyncDir flushes a directory, so that a rename in it survives a power cut.
func SyncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

// Mkdir creates a directory with the given mode and, if the process runs as
// root, the owner of its parent.
func Mkdir(path string, mode os.FileMode) error {
	if err := os.Mkdir(path, mode); err != nil {
		return err
	}
	OwnLike(path, filepath.Dir(path))
	return nil
}
