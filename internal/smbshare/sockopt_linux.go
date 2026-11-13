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

import "syscall"

// tcpUserTimeout is TCP_USER_TIMEOUT from linux/tcp.h. The syscall package
// does not define it.
const tcpUserTimeout = 0x12

// setUserTimeout sets TCP_USER_TIMEOUT on a listening socket. Accepted
// connections inherit it. Keepalive probes are only sent on a connection
// with nothing in flight, so they do not notice a client that vanished
// while the server was sending. This option does.
func setUserTimeout(_, _ string, c syscall.RawConn) error {
	var sockErr error
	err := c.Control(func(fd uintptr) {
		sockErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpUserTimeout, int(peerTimeout.Milliseconds()))
	})
	if err != nil {
		return err
	}
	return sockErr
}
