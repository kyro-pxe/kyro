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

package tftp

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

var files = map[string][]byte{
	"ipxe.efi":      pattern(1_000_003),
	"undionly.kpxe": pattern(70_000),
	"exact.bin":     pattern(4 * 512), // a multiple of the block size
	"empty.bin":     {},
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/255)
	}
	return b
}

func start(t *testing.T, edit func(*Server)) netip.AddrPort {
	t.Helper()
	probe, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.LocalAddr().(*net.UDPAddr).AddrPort()
	probe.Close()
	s := &Server{
		Addr:    addr,
		Lookup:  func(name string) ([]byte, bool) { d, ok := files[name]; return d, ok },
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Timeout: 100 * time.Millisecond,
	}
	if edit != nil {
		edit(s)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	// Wait until the port answers.
	for i := 0; i < 100; i++ {
		c, err := net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(addr))
		if err == nil {
			c.Write([]byte{0, 9})
			c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			_, err = c.Read(make([]byte, 100))
			c.Close()
			if err == nil {
				return addr
			}
		}
	}
	t.Fatal("the server did not start")
	return addr
}

func rrq(name string, options ...string) []byte {
	b := []byte{0, opRead}
	b = append(b, name...)
	b = append(b, 0)
	b = append(b, "octet\x00"...)
	for _, o := range options {
		b = append(b, o...)
		b = append(b, 0)
	}
	return b
}

// client is a TFTP client that does what the test tells it.
type client struct {
	t    *testing.T
	conn *net.UDPConn
	peer *net.UDPAddr // the server's transfer port, once known
}

func newClient(t *testing.T) *client {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &client{t: t, conn: conn}
}

func (c *client) read() (op uint16, body []byte, ok bool) {
	buf := make([]byte, 70000)
	c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, from, err := c.conn.ReadFromUDP(buf)
	if err != nil || n < 2 {
		return 0, nil, false
	}
	c.peer = from
	return binary.BigEndian.Uint16(buf), buf[2:n], true
}

func (c *client) ack(block uint16) {
	c.conn.WriteToUDP([]byte{0, opAck, byte(block >> 8), byte(block)}, c.peer)
}

// fetch downloads a file and returns it with the options the server
// acknowledged.
func (c *client) fetch(server netip.AddrPort, req []byte, block int) ([]byte, string, error) {
	c.conn.WriteToUDP(req, net.UDPAddrFromAddrPort(server))
	var out []byte
	oack := ""
	want := uint16(1)
	for {
		op, body, ok := c.read()
		if !ok {
			return out, oack, fmt.Errorf("no answer after %d bytes", len(out))
		}
		switch op {
		case opOAck:
			oack = strings.ReplaceAll(strings.TrimRight(string(body), "\x00"), "\x00", " ")
			c.ack(0)
		case opData:
			n := binary.BigEndian.Uint16(body)
			if n == want {
				out = append(out, body[2:]...)
				want++
			}
			c.ack(n)
			if n == want-1 && len(body)-2 < block {
				return out, oack, nil
			}
		case opError:
			return out, oack, fmt.Errorf("error %d: %s", binary.BigEndian.Uint16(body), strings.TrimRight(string(body[2:]), "\x00"))
		}
	}
}

func TestFetch(t *testing.T) {
	server := start(t, nil)
	tests := []struct {
		name    string
		req     []byte
		file    string
		block   int
		wantAck string
	}{
		{"plain", rrq("ipxe.efi"), "ipxe.efi", 512, ""},
		{"leading slash", rrq("/undionly.kpxe"), "undionly.kpxe", 512, ""},
		{"leading backslash, upper case", rrq(`\IPXE.EFI`), "ipxe.efi", 512, ""},
		{"netascii is sent unchanged", bytes.Replace(rrq("undionly.kpxe"), []byte("octet"), []byte("NETASCII"), 1), "undionly.kpxe", 512, ""},
		{"block size and size", rrq("ipxe.efi", "blksize", "1428", "tsize", "0"), "ipxe.efi", 1428, "blksize 1428 tsize 1000003"},
		{"block size capped to what fits a frame", rrq("ipxe.efi", "blksize", "65464"), "ipxe.efi", 1468, "blksize 1468"},
		{"small blocks", rrq("undionly.kpxe", "BLKSIZE", "64"), "undionly.kpxe", 64, "blksize 64"},
		{"size only", rrq("undionly.kpxe", "tsize", "0"), "undionly.kpxe", 512, "tsize 70000"},
		{"unknown options are not acknowledged", rrq("undionly.kpxe", "windowsize", "8", "timeout", "3"), "undionly.kpxe", 512, ""},
		{"unknown option next to a known one", rrq("undionly.kpxe", "windowsize", "8", "tsize", "0"), "undionly.kpxe", 512, "tsize 70000"},
		{"size that is a multiple of the block size", rrq("exact.bin"), "exact.bin", 512, ""},
		{"empty file", rrq("empty.bin"), "empty.bin", 512, ""},
	}
	for _, tt := range tests {
		got, oack, err := newClient(t).fetch(server, tt.req, tt.block)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if !bytes.Equal(got, files[tt.file]) {
			t.Errorf("%s: got %d bytes, want %d; content differs", tt.name, len(got), len(files[tt.file]))
		}
		if oack != tt.wantAck {
			t.Errorf("%s: acknowledged options %q, want %q", tt.name, oack, tt.wantAck)
		}
	}
}

func TestErrors(t *testing.T) {
	server := start(t, nil)
	tests := []struct {
		name string
		req  []byte
		want string
	}{
		{"unknown file", rrq("pxelinux.0"), "error 1"},
		{"path with a directory", rrq("boot/ipxe.efi"), "error 1"},
		{"path that climbs", rrq("../../etc/passwd"), "error 1"},
		{"absolute path", rrq("/etc/passwd"), "error 1"},
		{"empty name", rrq(""), "error 1"},
		{"name with a NUL trick", append([]byte{0, opRead}, "ipxe.efi\x00\x00octet\x00"...), "error 4"},
		{"write request", append([]byte{0, opWrite}, "ipxe.efi\x00octet\x00"...), "error 2"},
		{"unknown mode", bytes.Replace(rrq("ipxe.efi"), []byte("octet"), []byte("mail"), 1), "error 4"},
		{"no terminator", []byte{0, opRead, 'x'}, "error 4"},
		{"block size too small", rrq("ipxe.efi", "blksize", "4"), "error 8"},
		{"block size not a number", rrq("ipxe.efi", "blksize", "big"), "error 8"},
		{"option without a value", append(rrq("ipxe.efi"), "blksize\x00"...), "error 4"},
		{"a data packet", []byte{0, opData, 0, 1, 'x'}, "error 4"},
	}
	for _, tt := range tests {
		_, _, err := newClient(t).fetch(server, tt.req, 512)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %s", tt.name, err, tt.want)
		}
	}
}

// TestResendsAfterLoss drops acknowledgements and checks that the server
// sends the block again, and only that block.
func TestResendsAfterLoss(t *testing.T) {
	server := start(t, nil)
	c := newClient(t)
	c.conn.WriteToUDP(rrq("undionly.kpxe"), net.UDPAddrFromAddrPort(server))

	// Block 1 arrives; stay silent and it arrives again.
	for i := 0; i < 3; i++ {
		op, body, ok := c.read()
		if !ok || op != opData || binary.BigEndian.Uint16(body) != 1 {
			t.Fatalf("read %d: op %d ok %v", i, op, ok)
		}
	}
	// A duplicate acknowledgement of an old block must not make the
	// server send anything twice.
	c.ack(1)
	op, body, ok := c.read()
	if !ok || op != opData || binary.BigEndian.Uint16(body) != 2 {
		t.Fatalf("after ack 1: op %d", op)
	}
	c.ack(1)
	c.ack(1)
	c.ack(2)
	op, body, ok = c.read()
	if !ok || op != opData || binary.BigEndian.Uint16(body) != 3 {
		t.Fatalf("after duplicate acks: op %d block %d", op, binary.BigEndian.Uint16(body))
	}
}

func TestGivesUpOnSilentClient(t *testing.T) {
	server := start(t, func(s *Server) { s.Timeout, s.Retries = 20*time.Millisecond, 2 })
	c := newClient(t)
	c.conn.WriteToUDP(rrq("undionly.kpxe"), net.UDPAddrFromAddrPort(server))
	count := 0
	for {
		c.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		if _, _, err := c.conn.ReadFromUDP(make([]byte, 1000)); err != nil {
			break
		}
		count++
	}
	if count != 3 {
		t.Errorf("block sent %d times, want 3 (once and two retries)", count)
	}
}

func TestTransferLimit(t *testing.T) {
	server := start(t, func(s *Server) { s.MaxTransfers = 2; s.Timeout = time.Second })
	// Two clients start a transfer and stall.
	for i := 0; i < 2; i++ {
		c := newClient(t)
		c.conn.WriteToUDP(rrq("ipxe.efi"), net.UDPAddrFromAddrPort(server))
		if op, _, ok := c.read(); !ok || op != opData {
			t.Fatalf("client %d did not get data", i)
		}
	}
	_, _, err := newClient(t).fetch(server, rrq("ipxe.efi"), 512)
	if err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("third transfer: %v", err)
	}
}

func FuzzParseRequest(f *testing.F) {
	f.Add(rrq("ipxe.efi"))
	f.Add(rrq("/ipxe.efi", "blksize", "1468", "tsize", "0", "windowsize", "4"))
	f.Add([]byte{0, opWrite, 'x', 0, 'o', 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		req, code, err := parseRequest(data)
		if err != nil {
			if code < 0 || code > 8 {
				t.Fatalf("error code %d", code)
			}
			return
		}
		if !nameRE.MatchString(req.name) || strings.ContainsAny(req.name, `/\`) || strings.Contains(req.name, "..") && strings.HasPrefix(req.name, ".") {
			t.Fatalf("accepted the name %q", req.name)
		}
		if req.block < 8 || req.block > maxBlock {
			t.Fatalf("block size %d", req.block)
		}
	})
}
