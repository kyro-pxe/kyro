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

// Package dhcp answers the DHCP requests of network boot firmware.
//
// In proxy mode, the default, another DHCP server hands out addresses and
// Kyro only adds where to boot from. That needs no change to the existing
// network. Kyro then answers two kinds of request:
//
//   - a DHCPDISCOVER broadcast to port 67, with a DHCPOFFER that carries no
//     address, only the boot server and the boot file;
//   - a DHCPREQUEST sent to port 4011, which is where UEFI firmware and
//     iPXE ask the boot server again, with a DHCPACK of the same content.
//
// Only requests from PXE clients are answered. Everything else on port 67
// belongs to the real DHCP server.
//
// Replies to clients without an address are broadcast. That way binding the
// ports is the only privilege needed; a unicast to a client that has no
// address yet would need a raw socket.
package dhcp

import (
	"bytes"
	"log/slog"
	"net/netip"
	"strings"
)

// Firmware is the kind of program that sent a request.
type Firmware int

// The kinds of firmware Kyro tells apart.
const (
	FirmwareUnknown Firmware = iota
	FirmwareBIOS             // PC BIOS, x86
	FirmwareUEFI64           // UEFI, x86-64
	FirmwareIPXE             // iPXE, whatever it runs on
)

func (f Firmware) String() string {
	switch f {
	case FirmwareBIOS:
		return "BIOS"
	case FirmwareUEFI64:
		return "UEFI x86-64"
	case FirmwareIPXE:
		return "iPXE"
	}
	return "unsupported firmware"
}

// BootFiles says what each kind of firmware is told to load. An empty
// entry means that such clients get no answer.
type BootFiles struct {
	// BIOS and UEFI64 are file names for TFTP.
	BIOS   string
	UEFI64 string
	// IPXE is the URL of the script that iPXE runs.
	IPXE string
}

// Proxy decides how to answer in proxy mode. It has no sockets; Server
// connects it to the network.
type Proxy struct {
	// Addr is Kyro's own address, which clients boot from.
	Addr  netip.Addr
	Files BootFiles
	Log   *slog.Logger
}

// Classify says what kind of firmware sent a request. It returns
// FirmwareUnknown for anything that is not a PXE client.
func Classify(p *Packet) Firmware {
	class := string(p.Options[OptVendorClass])
	if !strings.HasPrefix(class, "PXEClient") {
		// "HTTPClient" is UEFI HTTP boot, which Kyro does not serve.
		return FirmwareUnknown
	}
	// iPXE says so in the user class. It also sends the architecture of
	// the firmware it was started from, which no longer matters.
	if bytes.Equal(p.Options[OptUserClass], []byte("iPXE")) {
		return FirmwareIPXE
	}
	arch := p.Options[OptClientArch]
	if len(arch) != 2 || arch[0] != 0 {
		return FirmwareUnknown
	}
	switch arch[1] {
	case 0:
		return FirmwareBIOS
	case 7, 9: // "EFI BC" and "EFI x86-64"; firmware uses either for x86-64
		return FirmwareUEFI64
	}
	return FirmwareUnknown
}

func (x *Proxy) file(fw Firmware) string {
	switch fw {
	case FirmwareBIOS:
		return x.Files.BIOS
	case FirmwareUEFI64:
		return x.Files.UEFI64
	case FirmwareIPXE:
		return x.Files.IPXE
	}
	return ""
}

// Reply returns the answer to a request, or nil if there is none. bootPort
// says whether the request arrived on port 4011 instead of port 67.
func (x *Proxy) Reply(req *Packet, bootPort bool) *Packet {
	if req.Op != opRequest {
		return nil
	}
	// On port 67 only the first step is ours. The DHCPREQUEST that follows
	// is addressed to the server that offered the address.
	reply := Offer
	if bootPort {
		if req.Type() != Request {
			return nil
		}
		reply = Ack
	} else if req.Type() != Discover {
		return nil
	}

	fw := Classify(req)
	file := x.file(fw)
	if file == "" {
		if fw != FirmwareUnknown || strings.HasPrefix(string(req.Options[OptVendorClass]), "PXEClient") {
			x.Log.Info("dhcp: no boot file for this kind of client", "mac", req.MAC(), "firmware", fw.String())
		}
		return nil
	}

	out := &Packet{
		Op:     opReply,
		HLen:   6,
		XID:    req.XID,
		Flags:  req.Flags | flagBroad,
		CIAddr: req.CIAddr,
		GIAddr: req.GIAddr,
		SIAddr: x.Addr,
		CHAddr: req.CHAddr,
		SName:  x.Addr.String(),
		File:   file,
		Options: map[byte][]byte{
			OptMessageType: {reply},
			OptServerID:    x.Addr.AsSlice(),
			// The server calls itself PXEClient too. That is how the
			// client recognises the answer as one for it.
			OptVendorClass: []byte("PXEClient"),
		},
	}
	if id := req.Options[OptClientUUID]; len(id) > 0 {
		out.Options[OptClientUUID] = id
	}
	if fw == FirmwareBIOS {
		// A BIOS wants to be told that there is nothing to discover and
		// that it should load the file. UEFI firmware is better off
		// without this option: it then asks on port 4011, which all of it
		// supports, while some of it ignores an offer that carries the
		// option.
		if bootPort {
			out.Options[OptVendorOptions] = []byte{71, 4, 0, 0, 0, 0, 255} // boot item: none
		} else {
			out.Options[OptVendorOptions] = []byte{6, 1, 8, 255} // discovery control: use the boot file
		}
	}
	x.Log.Info("dhcp: boot information sent", "mac", req.MAC(), "firmware", fw.String(), "file", file, "step", map[bool]string{false: "offer", true: "ack"}[bootPort])
	return out
}
