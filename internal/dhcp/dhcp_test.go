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

package dhcp

import (
	"bytes"
	"io"
	"log/slog"
	"net/netip"
	"reflect"
	"testing"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

var testProxy = &Proxy{
	Addr:  netip.MustParseAddr("192.168.77.1"),
	Files: BootFiles{BIOS: "undionly.kpxe", UEFI64: "ipxe.efi", IPXE: "http://192.168.77.1/boot.ipxe"},
	Log:   quiet,
}

// request builds the kind of message that firmware sends.
func request(msgType byte, arch uint16, edit func(*Packet)) *Packet {
	p := &Packet{
		Op:     opRequest,
		HLen:   6,
		XID:    0x12345678,
		CHAddr: [16]byte{0x52, 0x54, 0x00, 0x77, 0x00, 0x01},
		Options: map[byte][]byte{
			OptMessageType: {msgType},
			OptVendorClass: []byte("PXEClient:Arch:00007:UNDI:003016"),
			OptClientArch:  {byte(arch >> 8), byte(arch)},
			OptClientUUID:  append([]byte{0}, bytes.Repeat([]byte{0xab}, 16)...),
		},
	}
	if edit != nil {
		edit(p)
	}
	return p
}

func ipxe(p *Packet) { p.Options[OptUserClass] = []byte("iPXE") }

func TestPacketRoundTrip(t *testing.T) {
	p := request(Discover, 7, func(p *Packet) {
		p.Flags = flagBroad
		p.Secs = 4
		p.CIAddr = netip.MustParseAddr("10.0.0.5")
		p.GIAddr = netip.MustParseAddr("10.0.0.1")
		p.SName = "server"
		p.File = "ipxe.efi"
		p.Options[200] = bytes.Repeat([]byte{7}, 300) // longer than one option holds
	})
	wire, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) < minReplyLn {
		t.Errorf("message of %d bytes is shorter than the BOOTP minimum", len(wire))
	}
	got, err := Parse(wire)
	if err != nil {
		t.Fatal(err)
	}
	// Unset addresses come back as 0.0.0.0.
	p.YIAddr, p.SIAddr = netip.IPv4Unspecified(), netip.IPv4Unspecified()
	if !reflect.DeepEqual(got, p) {
		t.Errorf("round trip changed the message:\n got %+v\nwant %+v", got, p)
	}
	if got.MAC() != "52:54:00:77:00:01" || got.Type() != Discover {
		t.Errorf("MAC %q, type %d", got.MAC(), got.Type())
	}
	// The message type is the first option, as some firmware expects.
	if wire[minLen] != OptMessageType {
		t.Errorf("first option is %d", wire[minLen])
	}
}

func TestParseRejects(t *testing.T) {
	good, _ := request(Discover, 7, nil).Marshal()
	cases := map[string]func([]byte) []byte{
		"empty":             func(b []byte) []byte { return nil },
		"too short":         func(b []byte) []byte { return b[:minLen-1] },
		"too long":          func(b []byte) []byte { return append(b, make([]byte, 2000)...) },
		"no magic cookie":   func(b []byte) []byte { b[headerLen] = 0; return b },
		"not Ethernet":      func(b []byte) []byte { b[1] = 6; return b },
		"address length 16": func(b []byte) []byte { b[2] = 16; return b },
		"option cut off":    func(b []byte) []byte { return append(b[:minLen], 60, 200, 'x') },
		"option length cut": func(b []byte) []byte { return append(b[:minLen], 60) },
	}
	for name, edit := range cases {
		if _, err := Parse(edit(bytes.Clone(good))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Padding and a missing end marker are tolerated.
	b := append(bytes.Clone(good[:minLen]), 0, 0, OptMessageType, 1, Discover, 0)
	if p, err := Parse(b); err != nil || p.Type() != Discover {
		t.Errorf("padded options: %v", err)
	}
}

func TestParseOptionOverload(t *testing.T) {
	b := make([]byte, minLen)
	b[0], b[1], b[2] = opRequest, 1, 6
	b[236], b[237], b[238], b[239] = 0x63, 0x82, 0x53, 0x63
	// The boot file field carries options; the server name is a name.
	copy(b[108:], []byte{OptVendorClass, 9, 'P', 'X', 'E', 'C', 'l', 'i', 'e', 'n', 't', 255})
	copy(b[44:], "name")
	b = append(b, OptOverload, 1, 1, OptMessageType, 1, Discover, 255)
	p, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Options[OptVendorClass]) != "PXEClient" || p.File != "" || p.SName != "name" {
		t.Errorf("options %v, file %q, server name %q", p.Options, p.File, p.SName)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		p    *Packet
		want Firmware
	}{
		{"BIOS", request(Discover, 0, nil), FirmwareBIOS},
		{"UEFI x64 as type 7", request(Discover, 7, nil), FirmwareUEFI64},
		{"UEFI x64 as type 9", request(Discover, 9, nil), FirmwareUEFI64},
		{"iPXE on UEFI", request(Discover, 7, ipxe), FirmwareIPXE},
		{"iPXE on BIOS", request(Discover, 0, ipxe), FirmwareIPXE},
		{"UEFI IA32", request(Discover, 6, nil), FirmwareUnknown},
		{"ARM64", request(Discover, 11, nil), FirmwareUnknown},
		{"UEFI HTTP boot", request(Discover, 16, func(p *Packet) { p.Options[OptVendorClass] = []byte("HTTPClient:Arch:00016") }), FirmwareUnknown},
		{"an ordinary DHCP client", request(Discover, 7, func(p *Packet) { p.Options = map[byte][]byte{OptMessageType: {Discover}} }), FirmwareUnknown},
		{"Windows", request(Discover, 7, func(p *Packet) { p.Options[OptVendorClass] = []byte("MSFT 5.0") }), FirmwareUnknown},
		{"no architecture", request(Discover, 7, func(p *Packet) { delete(p.Options, OptClientArch) }), FirmwareUnknown},
		{"iPXE without PXEClient", request(Discover, 7, func(p *Packet) { ipxe(p); delete(p.Options, OptVendorClass) }), FirmwareUnknown},
	}
	for _, tt := range tests {
		if got := Classify(tt.p); got != tt.want {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestProxyReply(t *testing.T) {
	// Port 67: an offer without an address.
	r := testProxy.Reply(request(Discover, 7, nil), false)
	if r == nil {
		t.Fatal("no offer for a UEFI client")
	}
	if r.Op != opReply || r.Type() != Offer || r.XID != 0x12345678 || r.File != "ipxe.efi" ||
		r.SIAddr != testProxy.Addr || r.SName != "192.168.77.1" || r.MAC() != "52:54:00:77:00:01" {
		t.Errorf("offer = %+v", r)
	}
	if r.YIAddr.IsValid() && !r.YIAddr.IsUnspecified() {
		t.Errorf("a proxy offer must not hand out an address, got %v", r.YIAddr)
	}
	if string(r.Options[OptVendorClass]) != "PXEClient" || !bytes.Equal(r.Options[OptServerID], []byte{192, 168, 77, 1}) {
		t.Errorf("options = %v", r.Options)
	}
	if !bytes.Equal(r.Options[OptClientUUID], request(Discover, 7, nil).Options[OptClientUUID]) {
		t.Error("the client's UUID is not echoed")
	}
	if r.Flags&flagBroad == 0 {
		t.Error("broadcast flag not set")
	}
	if _, ok := r.Options[OptVendorOptions]; ok {
		t.Error("a UEFI client got PXE vendor options")
	}
	for _, opt := range []byte{OptSubnetMask, OptRouter, OptDNS, OptLeaseTime} {
		if _, ok := r.Options[opt]; ok {
			t.Errorf("a proxy offer carries option %d, which belongs to the real DHCP server", opt)
		}
	}

	// A BIOS gets the option that tells it to just load the file.
	r = testProxy.Reply(request(Discover, 0, nil), false)
	if r == nil || r.File != "undionly.kpxe" || !bytes.Equal(r.Options[OptVendorOptions], []byte{6, 1, 8, 255}) {
		t.Errorf("BIOS offer = %+v", r)
	}
	// iPXE gets the script.
	r = testProxy.Reply(request(Discover, 7, ipxe), false)
	if r == nil || r.File != "http://192.168.77.1/boot.ipxe" {
		t.Errorf("iPXE offer = %+v", r)
	}

	// Port 4011: an acknowledgement with the same content.
	r = testProxy.Reply(request(Request, 9, nil), true)
	if r == nil || r.Type() != Ack || r.File != "ipxe.efi" {
		t.Errorf("ack = %+v", r)
	}
	r = testProxy.Reply(request(Request, 7, ipxe), true)
	if r == nil || r.Type() != Ack || r.File != "http://192.168.77.1/boot.ipxe" {
		t.Errorf("iPXE ack = %+v", r)
	}
}

func TestProxyStaysSilent(t *testing.T) {
	tests := []struct {
		name     string
		p        *Packet
		bootPort bool
	}{
		{"request on port 67 (for the real server)", request(Request, 7, nil), false},
		{"inform on port 67", request(Inform, 7, nil), false},
		{"release", request(Release, 7, nil), false},
		{"discover on port 4011", request(Discover, 7, nil), true},
		{"no message type", request(0, 7, func(p *Packet) { delete(p.Options, OptMessageType) }), false},
		{"a reply", request(Discover, 7, func(p *Packet) { p.Op = opReply }), false},
		{"an ordinary DHCP client", request(Discover, 7, func(p *Packet) { p.Options = map[byte][]byte{OptMessageType: {Discover}} }), false},
		{"Windows renewing its lease", request(Discover, 7, func(p *Packet) { p.Options[OptVendorClass] = []byte("MSFT 5.0") }), false},
		{"UEFI on ARM", request(Discover, 11, nil), false},
		{"UEFI HTTP boot", request(Discover, 16, func(p *Packet) { p.Options[OptVendorClass] = []byte("HTTPClient") }), false},
	}
	for _, tt := range tests {
		if r := testProxy.Reply(tt.p, tt.bootPort); r != nil {
			t.Errorf("%s: answered with %+v", tt.name, r)
		}
	}
	// Without a file for BIOS clients, BIOS clients get nothing.
	noBIOS := &Proxy{Addr: testProxy.Addr, Files: BootFiles{UEFI64: "ipxe.efi"}, Log: quiet}
	if r := noBIOS.Reply(request(Discover, 0, nil), false); r != nil {
		t.Errorf("BIOS client answered without a BIOS boot file: %+v", r)
	}
}

func FuzzParse(f *testing.F) {
	for _, p := range []*Packet{request(Discover, 7, nil), request(Request, 0, ipxe)} {
		b, _ := p.Marshal()
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := Parse(data)
		if err != nil {
			return
		}
		p.MAC()
		p.Type()
		// What was read can be written and read again to the same effect.
		wire, err := p.Marshal()
		if err != nil {
			return // too large to fit once the options are laid out
		}
		again, err := Parse(wire)
		if err != nil {
			t.Fatalf("cannot parse what Marshal wrote: %v", err)
		}
		delete(p.Options, OptOverload)
		delete(again.Options, OptOverload)
		for code, v := range p.Options {
			if code != 0 && code != 255 && !bytes.Equal(again.Options[code], v) {
				t.Fatalf("option %d changed in a round trip", code)
			}
		}
	})
}

// FuzzProxy gives arbitrary messages to the proxy. Whatever it answers
// must be a boot answer and nothing more: no address, no network settings,
// only to PXE clients.
func FuzzProxy(f *testing.F) {
	for _, p := range []*Packet{request(Discover, 7, nil), request(Request, 0, ipxe), request(Discover, 9, nil)} {
		b, _ := p.Marshal()
		f.Add(b, false)
		f.Add(b, true)
	}
	f.Fuzz(func(t *testing.T, data []byte, bootPort bool) {
		req, err := Parse(data)
		if err != nil {
			return
		}
		r := testProxy.Reply(req, bootPort)
		if r == nil {
			return
		}
		if !bytes.HasPrefix(req.Options[OptVendorClass], []byte("PXEClient")) {
			t.Fatal("answered a client that is not a PXE client")
		}
		if r.Op != opReply || r.XID != req.XID || r.CHAddr != req.CHAddr {
			t.Fatalf("reply does not belong to the request: %+v", r)
		}
		if r.YIAddr.IsValid() && !r.YIAddr.IsUnspecified() {
			t.Fatal("the proxy handed out an address")
		}
		for _, opt := range []byte{OptSubnetMask, OptRouter, OptDNS, OptLeaseTime} {
			if _, ok := r.Options[opt]; ok {
				t.Fatalf("the proxy sent option %d", opt)
			}
		}
		if _, err := r.Marshal(); err != nil {
			t.Fatalf("reply cannot be written: %v", err)
		}
	})
}
