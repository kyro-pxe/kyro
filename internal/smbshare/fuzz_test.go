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

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jfjallid/go-smb/smb"
	"github.com/jfjallid/go-smb/smb/server"
)

// The fuzz tests in this file send bytes to a running server, the way a
// client on the boot network could. They cover the SMB library together
// with this package. A run fails if
//
//   - the server logs an error, which includes a recovered panic,
//   - the server does not finish with a connection after the client
//     closed it,
//   - a file or directory stays open after its connection ended, or
//   - the process crashes or runs out of memory.
//
// The account is public, so messages inside a logged-in session are as
// reachable for an attacker as messages before the login. Both are fuzzed.

// knownPanics lists the functions of the SMB library, at the pinned version,
// that are known to panic on malformed input. The library recovers and
// closes the connection. The fuzz tests tolerate these so that they can go
// on and find other problems; any other panic fails the test.
//
// docs/adr/0003-smb-server-library.md describes each entry. Remove an entry
// when the library is updated to a version that fixes it.
var knownPanics = []string{
	"github.com/jfjallid/go-smb/smb.(*CreateReq).CreateReqContexts",
	"github.com/jfjallid/go-smb/smb.(*CreateReq).CreateReqName",
	"github.com/jfjallid/go-smb/smb.(*NegotiateReq).UnmarshalBinary",
}

func isKnownPanic(record string) bool {
	if !strings.Contains(record, "recovered from panic") {
		return false
	}
	// The frame that panicked follows the runtime's own frames.
	_, after, found := strings.Cut(record, "\npanic(")
	if !found {
		return false
	}
	// The first line is the rest of the panic line itself.
	for _, line := range strings.Split(after, "\n")[1:] {
		if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "runtime.") || strings.HasPrefix(line, "panic(") {
			continue
		}
		for _, fn := range knownPanics {
			if strings.HasPrefix(line, fn+"(") {
				return true
			}
		}
		return false
	}
	return false
}

// errorLog is a slog.Handler that keeps what the server logs as an error.
type errorLog struct {
	mu      sync.Mutex
	records []string
}

func (e *errorLog) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelError }
func (e *errorLog) WithAttrs([]slog.Attr) slog.Handler           { return e }
func (e *errorLog) WithGroup(string) slog.Handler                { return e }
func (e *errorLog) Handle(_ context.Context, r slog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	msg := r.Message
	r.Attrs(func(a slog.Attr) bool {
		msg += "\n" + a.Key + ":\n" + a.Value.String()
		return true
	})
	if !isKnownPanic(msg) {
		e.records = append(e.records, msg)
	}
	return nil
}

func (e *errorLog) take() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.records
	e.records = nil
	return out
}

// fuzzServer is a server whose connections can be awaited.
type fuzzServer struct {
	port   int
	errors *errorLog
	table  *handleTable
	gone   chan struct{} // one value per connection that the server finished
	seen   chan []byte   // every request, if capture is set
}

func startFuzzServer(tb testing.TB, capture bool) *fuzzServer {
	tb.Helper()
	fz := &fuzzServer{errors: &errorLog{}, gone: make(chan struct{}, 1024)}
	srv, err := New(Config{
		Shares:   map[string]fs.FS{testShare: testTree()},
		Username: testUser,
		Password: testPassword,
		Logger:   slog.New(fz.errors),
	})
	if err != nil {
		tb.Fatal(err)
	}
	// Every recovered panic must reach the test, not five per minute.
	srv.srv.Config.Logger = libraryLogger{log: slog.New(fz.errors)}
	onDisconnect := srv.srv.Config.OnDisconnect
	srv.srv.Config.OnDisconnect = func(c *server.Conn) {
		onDisconnect(c)
		fz.gone <- struct{}{}
	}
	fz.table = srv.srv.Config.Shares[testShare].VFS.(*shareFS).handles
	if capture {
		fz.seen = make(chan []byte, 1024)
		srv.srv.Config.OnRawRequest = func(_ *server.Conn, raw []byte) (bool, error) {
			fz.seen <- bytes.Clone(raw)
			return false, nil
		}
	}
	l, err := Listen("127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	go srv.Serve(l)
	tb.Cleanup(func() { srv.Close() })
	fz.port = l.Addr().(*net.TCPAddr).Port
	return fz
}

// finished waits until the server is done with one connection, and fails
// the test if that takes too long or if the server logged an error.
func (fz *fuzzServer) finished(t *testing.T) {
	t.Helper()
	select {
	case <-fz.gone:
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not finish with the connection within 10 seconds")
	}
	// The tests use one connection at a time, so nothing may be open now.
	if n := fz.table.count(); n != 0 {
		t.Fatalf("%d handles are still open after the connection ended", n)
	}
	if errs := fz.errors.take(); len(errs) > 0 {
		t.Fatalf("the server logged errors:\n%s", strings.Join(errs, "\n"))
	}
}

// realTraffic records the requests of a client that uses the share the way
// Windows Setup does, plus a few things it is not allowed to do. They are
// the starting points for the fuzzer.
func realTraffic(tb testing.TB) [][]byte {
	tb.Helper()
	fz := startFuzzServer(tb, true)
	conn, err := smb.NewConnection(clientOptions(fz.port))
	if err != nil {
		tb.Fatal(err)
	}
	conn.TreeConnect(testShare)
	conn.ListDirectory(testShare, "", "*")
	conn.ListDirectory(testShare, "sources", `install"*`)
	readAll(conn, testShare, `sources\install.wim`)
	if f, err := conn.OpenFile(testShare, `sources\ei.cfg`); err == nil {
		f.QueryInfoSecurity(4096)
		f.Flush()
		f.CloseFile()
	}
	payload := bytes.NewReader([]byte("data"))
	conn.PutFile(testShare, "new.txt", 0, payload.Read)
	conn.Mkdir(testShare, "newdir")
	conn.DeleteFile(testShare, "setup.exe")
	conn.Echo()
	conn.TreeDisconnect(testShare)
	conn.Logoff()
	conn.Close()
	<-fz.gone

	var out [][]byte
	for {
		select {
		case raw := <-fz.seen:
			out = append(out, raw)
		default:
			if len(out) < 8 {
				tb.Fatalf("recorded only %d requests", len(out))
			}
			return out
		}
	}
}

// isSMB2 reports whether a recorded request is an SMB2 message. The client
// library opens with an SMB1 negotiation, as Windows does.
func isSMB2(raw []byte) bool {
	return len(raw) >= 64 && string(raw[:4]) == "\xfeSMB"
}

// firstSMB2 returns the index of the first SMB2 message in the traffic.
func firstSMB2(tb testing.TB, traffic [][]byte) int {
	tb.Helper()
	for i, raw := range traffic {
		if isSMB2(raw) {
			return i
		}
	}
	tb.Fatal("no SMB2 message recorded")
	return 0
}

// frame puts the length prefix of SMB over TCP in front of a message.
func frame(msg []byte) []byte {
	out := make([]byte, 4, 4+len(msg))
	binary.BigEndian.PutUint32(out, uint32(len(msg))&0x00ffffff)
	return append(out, msg...)
}

// sendRaw writes bytes to a new connection, closes the sending side and
// waits until the server is done with it.
func (fz *fuzzServer) sendRaw(t *testing.T, data []byte) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(fz.port)), 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	go io.Copy(io.Discard, conn)
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	conn.Write(data) // the server may hang up early; that is fine
	conn.(*net.TCPConn).CloseWrite()
	fz.finished(t)
}

// FuzzStream sends arbitrary bytes to the port. This reaches the framing
// and whatever the first message turns out to be.
func FuzzStream(f *testing.F) {
	traffic := realTraffic(f)
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{0, 0xff, 0xff, 0xff})
	f.Add([]byte("\x00\x00\x00\x04\xffSMB"))
	f.Add([]byte("\x00\x00\x00\x04\xfeSMB"))
	f.Add([]byte("\x00\x00\x00\x04\xfdSMB"))
	f.Add([]byte("\x00\x00\x00\x04\xfcSMB"))
	f.Add(frame(traffic[0]))
	f.Add(append(frame(traffic[0]), frame(traffic[1])...))

	fz := startFuzzServer(f, false)
	f.Fuzz(func(t *testing.T, data []byte) {
		fz.sendRaw(t, data)
	})
}

// FuzzNegotiateAndLogin sends up to three well-framed messages to a new
// connection: what a client sends before it is logged in. The starting
// points are a real negotiation and a real login.
func FuzzNegotiateAndLogin(f *testing.F) {
	traffic := realTraffic(f)
	i := firstSMB2(f, traffic)
	f.Add(traffic[i], traffic[i+1], traffic[i+2]) // negotiation and both steps of the login
	f.Add(traffic[i], traffic[i+1], []byte{})
	f.Add(traffic[i], []byte{}, []byte{})
	f.Add(traffic[0], traffic[i], traffic[i+1]) // with the SMB1 negotiation in front
	f.Add(traffic[i+1], traffic[i+2], traffic[i+3])

	fz := startFuzzServer(f, false)
	f.Fuzz(func(t *testing.T, a, b, c []byte) {
		var data []byte
		for _, msg := range [][]byte{a, b, c} {
			if len(msg) > 0 {
				data = append(data, frame(msg)...)
			}
		}
		fz.sendRaw(t, data)
	})
}

// fileIDOffset is where a request of the given command carries the ID of
// the file it refers to, counted from the end of the header.
var fileIDOffset = map[uint16]int{
	smb.CommandClose:          8,
	smb.CommandFlush:          8,
	smb.CommandRead:           16,
	smb.CommandWrite:          16,
	smb.CommandLock:           8,
	smb.CommandIOCtl:          8,
	smb.CommandQueryDirectory: 8,
	smb.CommandChangeNotify:   8,
	smb.CommandQueryInfo:      24,
	smb.CommandSetInfo:        16,
	smb.CommandOplockBreak:    8,
}

// FuzzSession sends one message inside a logged-in, signed session with an
// open file and an open directory. The fuzzer chooses the command and the
// body; the test fills in the session, the share and, if asked to, the ID
// of the open file or directory, since a guessed ID would never be valid.
func FuzzSession(f *testing.F) {
	for _, raw := range realTraffic(f) {
		if !isSMB2(raw) {
			continue
		}
		cmd := binary.LittleEndian.Uint16(raw[12:14])
		for _, target := range []uint8{0, 1, 2} {
			f.Add(cmd, target, raw[64:])
		}
	}

	fz := startFuzzServer(f, false)
	f.Fuzz(func(t *testing.T, cmd uint16, target uint8, body []byte) {
		conn, err := smb.NewConnection(clientOptions(fz.port))
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		closed := false
		closeConn := func() {
			if !closed {
				closed = true
				conn.Close()
				fz.finished(t)
			}
		}
		defer closeConn()

		file, err := conn.OpenFile(testShare, `sources\install.wim`)
		if err != nil {
			t.Fatalf("open file: %v", err)
		}
		opts := smb.NewCreateReqOpts()
		opts.CreateOpts = smb.FileDirectoryFile
		dir, err := conn.OpenFileExt(testShare, "sources", opts)
		if err != nil {
			t.Fatalf("open directory: %v", err)
		}

		body = bytes.Clone(body)
		if off, ok := fileIDOffset[cmd]; ok && len(body) >= off+16 {
			switch target % 3 {
			case 1:
				copy(body[off:], file.FileID())
			case 2:
				copy(body[off:], dir.FileID())
			}
		}

		msg := make([]byte, 64, 64+len(body))
		copy(msg, "\xfeSMB")
		binary.LittleEndian.PutUint16(msg[4:], 64)   // header size
		binary.LittleEndian.PutUint16(msg[6:], 1)    // credit charge
		binary.LittleEndian.PutUint16(msg[12:], cmd) // command
		binary.LittleEndian.PutUint16(msg[14:], 1)   // credits requested
		binary.LittleEndian.PutUint32(msg[36:], conn.TreeID(testShare))
		binary.LittleEndian.PutUint64(msg[40:], conn.SessionID())
		msg = append(msg, body...)

		// Some requests get no reply, or none until something changes.
		// Do not wait for those.
		replied := make(chan struct{})
		go func() {
			conn.SendRawPDU(msg)
			close(replied)
		}()
		wait := 3 * time.Second
		if cmd == smb.CommandCancel || cmd == smb.CommandChangeNotify || cmd == smb.CommandLock {
			wait = 20 * time.Millisecond
		}
		select {
		case <-replied:
		case <-time.After(wait):
		}
		closeConn()
		<-replied
	})
}

// TestFuzzHarnessWorks checks the fuzz tests themselves: the recorded
// traffic has the expected shape, and a correct request through the
// session harness gets the correct answer.
func TestFuzzHarnessWorks(t *testing.T) {
	traffic := realTraffic(t)
	commands := map[uint16]bool{}
	for _, raw := range traffic {
		if isSMB2(raw) {
			commands[binary.LittleEndian.Uint16(raw[12:14])] = true
		}
	}
	for _, cmd := range []uint16{
		smb.CommandNegotiate, smb.CommandSessionSetup, smb.CommandTreeConnect, smb.CommandCreate,
		smb.CommandClose, smb.CommandRead, smb.CommandQueryDirectory, smb.CommandQueryInfo,
		smb.CommandEcho, smb.CommandTreeDisconnect, smb.CommandLogoff,
	} {
		if !commands[cmd] {
			t.Errorf("the recorded traffic has no request with command %d", cmd)
		}
	}
	first := traffic[firstSMB2(t, traffic)]
	if binary.LittleEndian.Uint16(first[12:14]) != smb.CommandNegotiate {
		t.Error("the first recorded SMB2 request is not the negotiation")
	}

	// A READ built the way FuzzSession builds messages returns file data.
	fz := startFuzzServer(t, false)
	conn, err := smb.NewConnection(clientOptions(fz.port))
	if err != nil {
		t.Fatal(err)
	}
	file, err := conn.OpenFile(testShare, `sources\install.wim`)
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 49)
	binary.LittleEndian.PutUint16(body[0:], 49) // structure size
	binary.LittleEndian.PutUint32(body[4:], 16) // length
	copy(body[16:], file.FileID())
	msg := make([]byte, 64, 64+len(body))
	copy(msg, "\xfeSMB")
	binary.LittleEndian.PutUint16(msg[4:], 64)
	binary.LittleEndian.PutUint16(msg[6:], 1)
	binary.LittleEndian.PutUint16(msg[12:], smb.CommandRead)
	binary.LittleEndian.PutUint16(msg[14:], 1)
	binary.LittleEndian.PutUint32(msg[36:], conn.TreeID(testShare))
	binary.LittleEndian.PutUint64(msg[40:], conn.SessionID())
	reply, err := conn.SendRawPDU(append(msg, body...))
	if err != nil {
		t.Fatalf("read through the harness: %v", err)
	}
	if !bytes.HasSuffix(reply, []byte("0123456789abcdef")) {
		t.Errorf("reply does not end with the file content: % x", reply)
	}
	conn.Close()
	fz.finished(t)

	// Garbage before the login is survived and noticed as finished.
	fz.sendRaw(t, []byte("\x00\x00\x00\x08\xfeSMBjunk"))
	fz.sendRaw(t, nil)
}
