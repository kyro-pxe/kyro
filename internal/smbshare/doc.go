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

// Package smbshare serves directory trees as read-only SMB shares.
//
// Windows Setup, started from Windows PE, reads the install image from such
// a share. See docs/adr/0001-windows-install-source.md for why it is SMB
// and docs/adr/0003-smb-server-library.md for how this package is built.
//
// The SMB protocol itself comes from github.com/jfjallid/go-smb. This
// package adds the parts that decide what a client can reach: the file
// system behind the share, the search patterns, the server settings and the
// limits.
//
// What the server offers is deliberately small:
//
//   - SMB 3.1.1 only, signing required, one named account, no guest and no
//     anonymous access.
//   - Reading only. The file system behind a share is an fs.FS, which has
//     no operation that changes anything.
//   - Directories and regular files only. Symbolic links are not followed
//     and not listed.
//
// The account is not a secret. Its name and password are written into a
// script that Kyro serves to every client over HTTP. It exists because
// signing needs an authenticated session.
package smbshare
