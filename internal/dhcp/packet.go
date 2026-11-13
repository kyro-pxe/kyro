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
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"sort"
)

// Message types (option 53).
const (
	Discover byte = 1
	Offer    byte = 2
	Request  byte = 3
	Decline  byte = 4
	Ack      byte = 5
	Nak      byte = 6
	Release  byte = 7
	Inform   byte = 8
)

// Option codes that Kyro reads or writes.
const (
	OptSubnetMask    byte = 1
	OptRouter        byte = 3
	OptDNS           byte = 6
	OptRequestedIP   byte = 50
	OptLeaseTime     byte = 51
	OptOverload      byte = 52
	OptMessageType   byte = 53
	OptServerID      byte = 54
	OptVendorClass   byte = 60
	OptBootFile      byte = 67
	OptUserClass     byte = 77
	OptClientArch    byte = 93
	OptClientUUID    byte = 97
	OptVendorOptions byte = 43
)

const (
	opRequest byte = 1
	opReply   byte = 2

	headerLen  = 236
	minLen     = headerLen + 4
	maxLen     = 1500 // more than fits into one Ethernet frame is not DHCP
	flagBroad  = 0x8000
	magic      = 0x63825363
	minReplyLn = 300 // BOOTP minimum, which some firmware insists on
)

// Packet is a DHCP message.
type Packet struct {
	Op     byte
	HLen   byte
	Hops   byte
	XID    uint32
	Secs   uint16
	Flags  uint16
	CIAddr netip.Addr // the address the client already has
	YIAddr netip.Addr // the address the client is given
	SIAddr netip.Addr // the server to boot from
	GIAddr netip.Addr // the relay the message came through
	CHAddr [16]byte   // the client's hardware address
	SName  string
	File   string
	// Options holds each option once. A message that repeats an option has
	// the parts joined, as RFC 3396 says.
	Options map[byte][]byte
}

// ErrNotDHCP is returned by Parse for anything that is not a DHCP message.
var ErrNotDHCP = errors.New("dhcp: not a DHCP message")

// Parse reads a DHCP message.
func Parse(b []byte) (*Packet, error) {
	if len(b) < minLen || len(b) > maxLen {
		return nil, ErrNotDHCP
	}
	if binary.BigEndian.Uint32(b[headerLen:]) != magic {
		return nil, ErrNotDHCP
	}
	// Ethernet only: hardware type 1, six bytes of address.
	if b[1] != 1 || b[2] != 6 {
		return nil, ErrNotDHCP
	}
	p := &Packet{
		Op:      b[0],
		HLen:    b[2],
		Hops:    b[3],
		XID:     binary.BigEndian.Uint32(b[4:]),
		Secs:    binary.BigEndian.Uint16(b[8:]),
		Flags:   binary.BigEndian.Uint16(b[10:]),
		CIAddr:  netip.AddrFrom4([4]byte(b[12:16])),
		YIAddr:  netip.AddrFrom4([4]byte(b[16:20])),
		SIAddr:  netip.AddrFrom4([4]byte(b[20:24])),
		GIAddr:  netip.AddrFrom4([4]byte(b[24:28])),
		Options: make(map[byte][]byte),
	}
	copy(p.CHAddr[:], b[28:44])

	if err := p.parseOptions(b[minLen:]); err != nil {
		return nil, err
	}
	// Option 52 says that the fields for the server name and the boot file
	// hold more options instead.
	sname, file := b[44:108], b[108:236]
	overload := byte(0)
	if v := p.Options[OptOverload]; len(v) == 1 {
		overload = v[0]
	}
	if overload&1 != 0 {
		if err := p.parseOptions(file); err != nil {
			return nil, err
		}
	} else {
		p.File = cstring(file)
	}
	if overload&2 != 0 {
		if err := p.parseOptions(sname); err != nil {
			return nil, err
		}
	} else {
		p.SName = cstring(sname)
	}
	return p, nil
}

func (p *Packet) parseOptions(b []byte) error {
	for len(b) > 0 {
		code := b[0]
		if code == 255 {
			return nil
		}
		if code == 0 {
			b = b[1:]
			continue
		}
		if len(b) < 2 || len(b) < 2+int(b[1]) {
			return fmt.Errorf("%w: option %d is cut off", ErrNotDHCP, code)
		}
		n := int(b[1])
		p.Options[code] = append(p.Options[code], b[2:2+n]...)
		b = b[2+n:]
	}
	return nil
}

func cstring(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// Marshal writes the message. Options are written in ascending order of
// their code, except the message type, which comes first. Option values
// longer than 255 bytes are split.
func (p *Packet) Marshal() ([]byte, error) {
	if len(p.SName) > 63 || len(p.File) > 127 {
		return nil, errors.New("dhcp: server name or boot file too long")
	}
	b := make([]byte, minLen, minReplyLn)
	b[0], b[1], b[2], b[3] = p.Op, 1, 6, p.Hops
	binary.BigEndian.PutUint32(b[4:], p.XID)
	binary.BigEndian.PutUint16(b[8:], p.Secs)
	binary.BigEndian.PutUint16(b[10:], p.Flags)
	for i, a := range []netip.Addr{p.CIAddr, p.YIAddr, p.SIAddr, p.GIAddr} {
		if a.Is4() {
			v := a.As4()
			copy(b[12+4*i:], v[:])
		}
	}
	copy(b[28:44], p.CHAddr[:])
	copy(b[44:108], p.SName)
	copy(b[108:236], p.File)
	binary.BigEndian.PutUint32(b[headerLen:], magic)

	codes := make([]int, 0, len(p.Options))
	for code := range p.Options {
		if code != 0 && code != 255 && code != OptMessageType {
			codes = append(codes, int(code))
		}
	}
	sort.Ints(codes)
	if _, ok := p.Options[OptMessageType]; ok {
		codes = append([]int{int(OptMessageType)}, codes...)
	}
	for _, code := range codes {
		v := p.Options[byte(code)]
		for first := true; first || len(v) > 0; first = false {
			n := min(len(v), 255)
			b = append(b, byte(code), byte(n))
			b = append(b, v[:n]...)
			v = v[n:]
		}
	}
	b = append(b, 255)
	if len(b) > maxLen {
		return nil, errors.New("dhcp: message too long")
	}
	for len(b) < minReplyLn {
		b = append(b, 0)
	}
	return b, nil
}

// Type returns the message type, or 0 if there is none.
func (p *Packet) Type() byte {
	if v := p.Options[OptMessageType]; len(v) == 1 {
		return v[0]
	}
	return 0
}

// MAC returns the client's hardware address in the form 52:54:00:12:34:56.
func (p *Packet) MAC() string {
	const hex = "0123456789abcdef"
	out := make([]byte, 0, 17)
	for i, c := range p.CHAddr[:6] {
		if i > 0 {
			out = append(out, ':')
		}
		out = append(out, hex[c>>4], hex[c&15])
	}
	return string(out)
}
