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
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"syscall"

	"golang.org/x/net/ipv4"
)

// Responder decides how to answer a request. Proxy is one.
type Responder interface {
	Reply(req *Packet, bootPort bool) *Packet
}

// Server connects a Responder to the network.
type Server struct {
	// Interface is the network interface that faces the clients. Requests
	// that arrive on another one are ignored.
	Interface string
	// Addr is Kyro's address on that interface.
	Addr      netip.Addr
	Responder Responder
	Log       *slog.Logger

	// The ports. Zero means the standard one: 67, 68 and 4011. Tests use
	// others.
	ServerPort, ClientPort, BootPort int
}

func (s *Server) ports() (server, client, boot int) {
	server, client, boot = s.ServerPort, s.ClientPort, s.BootPort
	if server == 0 {
		server = 67
	}
	if client == 0 {
		client = 68
	}
	if boot == 0 {
		boot = 4011
	}
	return
}

// Serve answers requests until ctx is done. It returns an error at once if
// a port cannot be opened.
func (s *Server) Serve(ctx context.Context) error {
	ifi, err := net.InterfaceByName(s.Interface)
	if err != nil {
		return fmt.Errorf("dhcp: interface %s: %w", s.Interface, err)
	}
	serverPort, _, bootPort := s.ports()

	// Port 67 has to be opened on all addresses, or broadcasts do not
	// arrive. The interface they arrive on is checked for each one.
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
			if serr == nil {
				serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
			}
		}); err != nil {
			return err
		}
		return serr
	}}
	main, err := lc.ListenPacket(ctx, "udp4", fmt.Sprintf(":%d", serverPort))
	if err != nil {
		return fmt.Errorf("dhcp: %w", err)
	}
	defer main.Close()
	boot, err := lc.ListenPacket(ctx, "udp4", netip.AddrPortFrom(s.Addr, uint16(bootPort)).String())
	if err != nil {
		return fmt.Errorf("dhcp: %w", err)
	}
	defer boot.Close()

	errs := make(chan error, 2)
	go func() { errs <- s.loop(ipv4.NewPacketConn(main), ifi.Index, false) }()
	go func() { errs <- s.loop(ipv4.NewPacketConn(boot), ifi.Index, true) }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-errs:
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("dhcp: %w", err)
	}
}

func (s *Server) loop(conn *ipv4.PacketConn, ifindex int, bootPort bool) error {
	if err := conn.SetControlMessage(ipv4.FlagInterface, true); err != nil {
		return err
	}
	_, clientPort, _ := s.ports()
	buf := make([]byte, maxLen+1)
	for {
		n, cm, from, err := conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if cm == nil || cm.IfIndex != ifindex {
			continue
		}
		req, err := Parse(buf[:n])
		if err != nil {
			continue
		}
		reply := s.Responder.Reply(req, bootPort)
		if reply == nil {
			continue
		}
		out, err := reply.Marshal()
		if err != nil {
			s.Log.Error("dhcp: cannot build a reply", "err", err)
			continue
		}

		// Where the reply goes: back through the relay if there was one,
		// to the client's address if it has one, to everyone otherwise.
		src, _ := from.(*net.UDPAddr)
		var to *net.UDPAddr
		switch {
		case req.GIAddr.IsValid() && !req.GIAddr.IsUnspecified():
			to = &net.UDPAddr{IP: req.GIAddr.AsSlice(), Port: 67}
		case bootPort && src != nil && !src.IP.IsUnspecified():
			to = src
		case req.CIAddr.IsValid() && !req.CIAddr.IsUnspecified():
			to = &net.UDPAddr{IP: req.CIAddr.AsSlice(), Port: clientPort}
		default:
			to = &net.UDPAddr{IP: net.IPv4bcast, Port: clientPort}
		}
		// The control message makes the reply leave through the boot
		// interface with Kyro's address on it, which matters for the
		// broadcast: the routing table has no say in where that goes.
		outCM := &ipv4.ControlMessage{IfIndex: ifindex, Src: s.Addr.AsSlice()}
		if _, err := conn.WriteTo(out, outCM, to); err != nil {
			s.Log.Warn("dhcp: cannot send a reply", "to", to.String(), "err", err)
		}
	}
}
