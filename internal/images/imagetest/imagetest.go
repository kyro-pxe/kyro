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

// Package imagetest makes stand-ins for Windows ISOs for tests. They have
// the files Kyro looks for and contain nothing from Microsoft.
package imagetest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kyro-pxe/kyro/internal/udf/udftest"
	"github.com/kyro-pxe/kyro/internal/wim"
)

// Editions are the editions of the stand-in install image.
var Editions = []wim.Edition{
	{Index: 1, Name: "Windows 11 Test Edition", EditionID: "Test", Build: "26100", Arch: "x64"},
	{Index: 2, Name: "Windows 11 Other Edition", EditionID: "Other", Build: "26100", Arch: "x64"},
}

// BootWIM is the content of sources/boot.wim in the stand-in.
var BootWIM = wim.Fake(nil, 5000)

// Files returns the tree of the stand-in ISO.
func Files() map[string][]byte {
	return map[string][]byte{
		"setup.exe":            []byte("MZ stand-in for setup.exe"),
		"sources/boot.wim":     BootWIM,
		"sources/install.wim":  wim.Fake(Editions, 9000),
		"sources/EI.CFG":       []byte("[Channel]\r\nRetail\r\n"),
		"efi/boot/bootx64.efi": []byte("stand-in"),
	}
}

// ISO returns a stand-in ISO.
func ISO() []byte { return udftest.Build(Files(), udftest.Options{}) }

// Write writes a stand-in ISO into a temporary directory and returns its
// path.
func Write(t testing.TB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "windows-standin.iso")
	if err := os.WriteFile(path, ISO(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
