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

// Package tftp is a read-only TFTP server.
//
// Network boot firmware loads its first program over TFTP because it knows
// nothing else. Kyro serves exactly the iPXE binaries that way; everything
// after that goes over HTTP.
//
// The server implements RFC 1350 reads with the options blksize and tsize
// (RFC 2347 to 2349). It cannot write: a write request is answered with an
// error. It serves no file system, only the files that a lookup function
// returns.
package tftp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	opRead  = 1
	opWrite = 2
	opData  = 3
	opAck   = 4
	opError = 5
	opOAck  = 6

	errNotFound = 1
	errAccess   = 2
	errIllegal  = 4
	errOption   = 8

	defaultBlock = 512
	// maxBlock is the largest block that fits an Ethernet frame without
	// being fragmented: 1500 minus the IP, UDP and TFTP headers.
	maxBlock = 1468
	// maxRequest bounds a request packet.
	maxRequest = 512
)

// Server is a TFTP server.
type Server struct {
	// Addr is the address to listen on. The standard port is 69.
	Addr netip.AddrPort
	// Lookup returns the content of a file, or false if there is none.
	// The name it gets is in lower case and has no directory part.
	Lookup func(name string) ([]byte, bool)
	Log    *slog.Logger

	// MaxTransfers limits the transfers at the same time. Zero means 64.
	MaxTransfers int
	// Timeout is how long to wait for an acknowledgement before sending a
	// block again, and Retries how often to do that. Zero means one second
	// and five times.
	Timeout time.Duration
	Retries int
}

// request is a parsed read request.
type request struct {
	name    string
	block   int  // block size to use
	options bool // whether the client sent options that were accepted
	blksize bool // whether the client asked for a block size
	tsize   bool // whether the client asked for the file size
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// parseRequest reads a read request. The error code is a TFTP error code.
func parseRequest(b []byte) (request, int, error) {
	if len(b) < 4 || len(b) > maxRequest {
		return request{}, errIllegal, errors.New("malformed request")
	}
	switch binary.BigEndian.Uint16(b) {
	case opRead:
	case opWrite:
		return request{}, errAccess, errors.New("this server is read-only")
	default:
		return request{}, errIllegal, errors.New("not a request")
	}
	// The rest is strings that each end with a zero byte: the file name,
	// the mode, and then pairs of option name and value.
	if b[len(b)-1] != 0 {
		return request{}, errIllegal, errors.New("malformed request")
	}
	fields := bytes.Split(b[2:len(b)-1], []byte{0})
	if len(fields) < 2 || len(fields)%2 != 0 {
		return request{}, errIllegal, errors.New("malformed request")
	}
	req := request{block: defaultBlock}

	// Firmware asks for "ipxe.efi", "/ipxe.efi" or "\ipxe.efi". Anything
	// with a directory in it is not a file this server has.
	name := strings.ToLower(strings.TrimLeft(string(fields[0]), `/\`))
	if !nameRE.MatchString(name) {
		return request{}, errNotFound, errors.New("file not found")
	}
	req.name = name
	switch strings.ToLower(string(fields[1])) {
	case "octet", "netascii": // netascii would change line ends; binaries are sent as they are
	default:
		return request{}, errIllegal, errors.New("unknown transfer mode")
	}
	for i := 2; i+1 < len(fields); i += 2 {
		value := string(fields[i+1])
		switch strings.ToLower(string(fields[i])) {
		case "blksize":
			n, err := strconv.Atoi(value)
			if err != nil || n < 8 || n > 65464 {
				return request{}, errOption, errors.New("bad blksize")
			}
			req.block, req.blksize, req.options = min(n, maxBlock), true, true
		case "tsize":
			req.tsize, req.options = true, true
		}
		// Other options (timeout, windowsize) are not acknowledged, which
		// tells the client that they do not apply.
	}
	return req, 0, nil
}

func errorPacket(code int, msg string) []byte {
	b := make([]byte, 4, 5+len(msg))
	binary.BigEndian.PutUint16(b, opError)
	binary.BigEndian.PutUint16(b[2:], uint16(code))
	return append(append(b, msg...), 0)
}

// Serve answers requests until ctx is done.
func (s *Server) Serve(ctx context.Context) error {
	conn, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(s.Addr))
	if err != nil {
		return fmt.Errorf("tftp: %w", err)
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	limit := s.MaxTransfers
	if limit <= 0 {
		limit = 64
	}
	slots := make(chan struct{}, limit)
	buf := make([]byte, maxRequest+1)
	for {
		n, client, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("tftp: %w", err)
		}
		req, code, err := parseRequest(buf[:n])
		if err != nil {
			conn.WriteToUDPAddrPort(errorPacket(code, err.Error()), client)
			continue
		}
		data, ok := s.Lookup(req.name)
		if !ok {
			s.Log.Info("tftp: file not found", "client", client.Addr().String(), "file", req.name)
			conn.WriteToUDPAddrPort(errorPacket(errNotFound, "file not found"), client)
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.WriteToUDPAddrPort(errorPacket(0, "too many transfers, try again"), client)
			continue
		}
		go func() {
			defer func() { <-slots }()
			start := time.Now()
			if err := s.send(ctx, client, req, data); err != nil {
				s.Log.Warn("tftp: transfer failed", "client", client.Addr().String(), "file", req.name, "err", err)
				return
			}
			s.Log.Info("tftp: file sent", "client", client.Addr().String(), "file", req.name, "bytes", len(data), "ms", time.Since(start).Milliseconds())
		}()
	}
}

// send transfers one file to one client, from a port of its own.
func (s *Server) send(ctx context.Context, client netip.AddrPort, req request, data []byte) error {
	conn, err := net.DialUDP("udp4", &net.UDPAddr{IP: s.Addr.Addr().AsSlice()}, net.UDPAddrFromAddrPort(client))
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	timeout, retries := s.Timeout, s.Retries
	if timeout <= 0 {
		timeout = time.Second
	}
	if retries <= 0 {
		retries = 5
	}
	// exchange sends a packet until the acknowledgement of the given block
	// arrives.
	reply := make([]byte, 516)
	exchange := func(packet []byte, block uint16) error {
		for try := 0; try <= retries; try++ {
			if _, err := conn.Write(packet); err != nil {
				return err
			}
			deadline := time.Now().Add(timeout)
			for {
				conn.SetReadDeadline(deadline)
				n, err := conn.Read(reply)
				if err != nil {
					var ne net.Error
					if errors.As(err, &ne) && ne.Timeout() {
						break // send again
					}
					return err
				}
				if n < 4 {
					continue
				}
				switch binary.BigEndian.Uint16(reply) {
				case opAck:
					// An acknowledgement of an earlier block is a duplicate
					// and must not trigger another send.
					if binary.BigEndian.Uint16(reply[2:]) == block {
						return nil
					}
				case opError:
					return fmt.Errorf("the client gave up: %q", strings.TrimRight(string(reply[4:n]), "\x00"))
				}
			}
		}
		return errors.New("the client stopped answering")
	}

	if req.options {
		// Only what the client asked for may be acknowledged.
		oack := []byte{0, opOAck}
		if req.blksize {
			oack = append(oack, "blksize\x00"+strconv.Itoa(req.block)+"\x00"...)
		}
		if req.tsize {
			oack = append(oack, "tsize\x00"+strconv.Itoa(len(data))+"\x00"...)
		}
		if err := exchange(oack, 0); err != nil {
			return err
		}
	}
	packet := make([]byte, 4, 4+req.block)
	for block, off := uint16(1), 0; ; block++ {
		chunk := data[off:min(off+req.block, len(data))]
		binary.BigEndian.PutUint16(packet, opData)
		binary.BigEndian.PutUint16(packet[2:], block)
		if err := exchange(append(packet[:4], chunk...), block); err != nil {
			return err
		}
		off += len(chunk)
		// A block that is not full ends the transfer. A file whose size is
		// a multiple of the block size ends with an empty block.
		if len(chunk) < req.block {
			return nil
		}
	}
}
