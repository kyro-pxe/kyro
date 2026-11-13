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

//go:build linux

package dhcp

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

// TestServerOnLoopback runs the server on the loopback interface with
// unprivileged ports. It covers the sockets; broadcasts to clients without
// an address are covered by the end-to-end test.
func TestServerOnLoopback(t *testing.T) {
	// Three free ports.
	var ports [3]int
	for i := range ports {
		c, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ports[i] = c.LocalAddr().(*net.UDPAddr).Port
		c.Close()
	}
	srv := &Server{
		Interface:  "lo",
		Addr:       netip.MustParseAddr("127.0.0.1"),
		Responder:  &Proxy{Addr: netip.MustParseAddr("127.0.0.1"), Files: testProxy.Files, Log: quiet},
		Log:        quiet,
		ServerPort: ports[0], ClientPort: ports[1], BootPort: ports[2],
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()

	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: ports[1]})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	exchange := func(req *Packet, port int) *Packet {
		t.Helper()
		wire, _ := req.Marshal()
		buf := make([]byte, 2000)
		// The server may not be listening yet on the first try.
		for try := 0; try < 20; try++ {
			client.WriteToUDP(wire, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
			client.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
			n, _, err := client.ReadFromUDP(buf)
			if err != nil {
				continue
			}
			reply, err := Parse(buf[:n])
			if err != nil {
				t.Fatalf("reply is not DHCP: %v", err)
			}
			return reply
		}
		return nil
	}

	// A client that has an address is answered at that address.
	discover := request(Discover, 7, func(p *Packet) { p.CIAddr = netip.MustParseAddr("127.0.0.1") })
	if r := exchange(discover, ports[0]); r == nil || r.Type() != Offer || r.File != "ipxe.efi" {
		t.Fatalf("offer = %+v", r)
	}
	// On the boot port the answer goes back to where the request came from.
	if r := exchange(request(Request, 7, ipxe), ports[2]); r == nil || r.Type() != Ack || r.File != testProxy.Files.IPXE {
		t.Fatalf("ack = %+v", r)
	}
	// Garbage and requests from other clients get nothing.
	client.WriteToUDP([]byte("hello"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: ports[0]})
	ordinary := request(Discover, 7, func(p *Packet) {
		p.CIAddr = netip.MustParseAddr("127.0.0.1")
		p.Options = map[byte][]byte{OptMessageType: {Discover}}
	})
	wire, _ := ordinary.Marshal()
	client.WriteToUDP(wire, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: ports[0]})
	client.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, _, err := client.ReadFromUDP(make([]byte, 2000)); err == nil {
		t.Errorf("got %d bytes in answer to requests that deserve none", n)
	}
}

func TestServeFailsOnMissingInterface(t *testing.T) {
	srv := &Server{Interface: "kyro-does-not-exist", Addr: netip.MustParseAddr("127.0.0.1"), Responder: testProxy, Log: quiet}
	if err := srv.Serve(context.Background()); err == nil {
		t.Error("Serve succeeded on an interface that does not exist")
	}
}
