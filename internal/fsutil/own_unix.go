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
	"syscall"
)

// OwnLike gives path the owner and group of ref, if the process runs as
// root. It does nothing otherwise, and it ignores errors: the worst case is
// a file that the administrator has to chown.
func OwnLike(path, ref string) {
	if os.Geteuid() != 0 {
		return
	}
	info, err := os.Stat(ref)
	if err != nil {
		return
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		os.Lchown(path, int(st.Uid), int(st.Gid))
	}
}
