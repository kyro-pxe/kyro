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

//go:build unix

package fsutil

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestMkdirStore(t *testing.T) {
	data := t.TempDir()
	dir := filepath.Join(data, "drivers")
	if err := MkdirStore(dir); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() || info.Mode().Perm() != 0o750 {
		t.Fatalf("stat: %v, %v", info, err)
	}
	// An existing directory is left as it is.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := MkdirStore(dir); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("an existing directory was changed: %v", info.Mode())
	}
	// A missing data directory is made too.
	if err := MkdirStore(filepath.Join(data, "a", "b", "profiles")); err != nil {
		t.Fatal(err)
	}
}

// As root, the CLI's case: the new directory belongs to the owner of the
// data directory, so that the service can read it.
func TestMkdirStoreOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to give a directory to another user")
	}
	data := t.TempDir()
	if err := os.Chown(data, 0, 65534); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(data, "drivers")
	if err := MkdirStore(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st := info.Sys().(*syscall.Stat_t); st.Uid != 0 || st.Gid != 65534 {
		t.Errorf("owner %d:%d, want 0:65534 like the data directory", st.Uid, st.Gid)
	}
}
