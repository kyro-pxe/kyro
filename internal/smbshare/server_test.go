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
	"crypto/rand"
	"errors"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jfjallid/go-smb/smb"
	"github.com/jfjallid/go-smb/spnego"
)

const (
	testUser     = "kyro"
	testPassword = "not-a-secret"
	testShare    = "win11"
)

// startServer runs a server on a loopback port and returns it with the
// port number.
func startServer(t testing.TB, cfg Config) (*Server, int) {
	t.Helper()
	if cfg.Username == "" {
		cfg.Username, cfg.Password = testUser, testPassword
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	l, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	t.Cleanup(func() {
		srv.Close()
		if err := <-done; !errors.Is(err, ErrServerClosed) {
			t.Errorf("Serve returned %v, want ErrServerClosed", err)
		}
	})
	return srv, l.Addr().(*net.TCPAddr).Port
}

func clientOptions(port int) smb.Options {
	return smb.Options{
		Host:                  "127.0.0.1",
		Port:                  port,
		Initiator:             &spnego.NTLMInitiator{User: testUser, Password: testPassword},
		RequireMessageSigning: true,
		DialTimeout:           5 * time.Second,
	}
}

func connect(t testing.TB, port int) *smb.Connection {
	t.Helper()
	conn, err := smb.NewConnection(clientOptions(port))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)
	if err := conn.TreeConnect(testShare); err != nil {
		t.Fatalf("tree connect: %v", err)
	}
	return conn
}

func readAll(conn *smb.Connection, share, path string) ([]byte, error) {
	var buf bytes.Buffer
	err := conn.RetrieveFile(share, path, 0, buf.Write)
	return buf.Bytes(), err
}

func listNames(t *testing.T, conn *smb.Connection, dir, pattern string) string {
	t.Helper()
	files, err := conn.ListDirectory(testShare, dir, pattern)
	if err != nil {
		t.Fatalf("list %q %q: %v", dir, pattern, err)
	}
	var out []string
	for _, f := range files {
		out = append(out, f.Name)
	}
	return strings.Join(out, " ")
}

func TestClientReadsShare(t *testing.T) {
	tree := testTree()
	big := make([]byte, 3<<20+123) // several reads
	rand.Read(big)
	tree["sources/install.wim"] = &fstest.MapFile{Data: big}
	_, port := startServer(t, Config{Shares: map[string]fs.FS{testShare: tree}})
	conn := connect(t, port)

	if got := conn.Dialect(); got != smb.DialectSmb_3_1_1 {
		t.Errorf("dialect = %#x, want SMB 3.1.1", got)
	}

	got, err := readAll(conn, testShare, `sources\install.wim`)
	if err != nil || !bytes.Equal(got, big) {
		t.Errorf("read install.wim: %d bytes, err %v; want %d identical bytes", len(got), err, len(big))
	}
	// The media say EI.CFG, Windows Setup asks for ei.cfg.
	got, err = readAll(conn, testShare, `SOURCES\ei.cfg`)
	if err != nil || string(got) != "[Channel]\r\nRetail\r\n" {
		t.Errorf("read ei.cfg = %q, %v", got, err)
	}
	if _, err := readAll(conn, testShare, `sources\missing.wim`); err == nil {
		t.Error("reading a missing file succeeded")
	}
	if _, err := readAll(conn, testShare, `..\..\etc\passwd`); err == nil {
		t.Error("reading outside the share succeeded")
	}

	if got := listNames(t, conn, "", "*"); got != "Empty boot setup.exe sources" {
		t.Errorf("root listing = %q", got)
	}
	if got := listNames(t, conn, "sources", "*"); got != "EI.CFG boot.wim install.wim sxs" {
		t.Errorf("sources listing = %q", got)
	}
	// What Windows sends for "dir install.*".
	if got := listNames(t, conn, "sources", `install"*`); got != "install.wim" {
		t.Errorf(`listing of install"* = %q`, got)
	}
	if got := listNames(t, conn, "sources", "*.esd"); got != "" {
		t.Errorf("listing of *.esd = %q", got)
	}
}

func TestClientCannotWrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "setup.exe"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	_, port := startServer(t, Config{Shares: map[string]fs.FS{testShare: root.FS()}})
	conn := connect(t, port)

	payload := bytes.NewReader([]byte("overwritten"))
	if err := conn.PutFile(testShare, "setup.exe", 0, payload.Read); err == nil {
		t.Error("overwriting a file succeeded")
	}
	payload.Reset([]byte("new"))
	if err := conn.PutFile(testShare, "new.txt", 0, payload.Read); err == nil {
		t.Error("creating a file succeeded")
	}
	if err := conn.Mkdir(testShare, "newdir"); err == nil {
		t.Error("creating a directory succeeded")
	}
	if err := conn.DeleteFile(testShare, "setup.exe"); err == nil {
		t.Error("deleting a file succeeded")
	}

	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "setup.exe" {
		t.Errorf("directory changed: %v, %v", entries, err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "setup.exe")); string(data) != "original" {
		t.Errorf("file changed: %q", data)
	}
	if got, err := readAll(conn, testShare, "setup.exe"); err != nil || string(got) != "original" {
		t.Errorf("read after the write attempts = %q, %v", got, err)
	}
}

func TestClientAuthentication(t *testing.T) {
	_, port := startServer(t, Config{Shares: map[string]fs.FS{testShare: testTree()}})

	tests := []struct {
		name      string
		initiator *spnego.NTLMInitiator
		ok        bool
	}{
		{"right account", &spnego.NTLMInitiator{User: testUser, Password: testPassword}, true},
		{"user name in another case", &spnego.NTLMInitiator{User: "KYRO", Password: testPassword}, true},
		{"wrong password", &spnego.NTLMInitiator{User: testUser, Password: "wrong"}, false},
		{"empty password", &spnego.NTLMInitiator{User: testUser, Password: ""}, false},
		{"unknown user", &spnego.NTLMInitiator{User: "administrator", Password: testPassword}, false},
		{"anonymous", &spnego.NTLMInitiator{AuthMode: spnego.NTLMAuthAnonymous}, false},
		{"guest", &spnego.NTLMInitiator{AuthMode: spnego.NTLMAuthGuest}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := clientOptions(port)
			opts.Initiator = tt.initiator
			opts.RequireMessageSigning = false
			conn, err := smb.NewConnection(opts)
			if err == nil {
				defer conn.Close()
				err = conn.TreeConnect(testShare)
			}
			if err == nil {
				_, err = readAll(conn, testShare, "setup.exe")
			}
			if (err == nil) != tt.ok {
				t.Errorf("access granted = %v, want %v (err: %v)", err == nil, tt.ok, err)
			}
		})
	}
}

func TestClientOlderDialectsRefused(t *testing.T) {
	_, port := startServer(t, Config{Shares: map[string]fs.FS{testShare: testTree()}})
	for _, dialect := range []uint16{smb.DialectSmb_2_0_2, smb.DialectSmb_2_1, smb.DialectSmb_3_0, smb.DialectSmb_3_0_2} {
		opts := clientOptions(port)
		opts.Dialects = []uint16{dialect}
		conn, err := smb.NewConnection(opts)
		if err == nil {
			conn.Close()
			t.Errorf("dialect %#x was accepted", dialect)
		}
	}
}

func TestClientUnsignedRefused(t *testing.T) {
	_, port := startServer(t, Config{Shares: map[string]fs.FS{testShare: testTree()}})
	opts := clientOptions(port)
	opts.RequireMessageSigning = false
	opts.DisableSigning = true
	conn, err := smb.NewConnection(opts)
	if err == nil {
		defer conn.Close()
		err = conn.TreeConnect(testShare)
	}
	if err == nil {
		_, err = readAll(conn, testShare, "setup.exe")
	}
	if err == nil {
		t.Error("a client that does not sign could read a file")
	}
}

func TestClientOtherShares(t *testing.T) {
	_, port := startServer(t, Config{Shares: map[string]fs.FS{testShare: testTree(), "Other": fstest.MapFS{"x": {Data: []byte("x")}}}})
	conn := connect(t, port)
	if err := conn.TreeConnect("missing"); err == nil {
		t.Error("connecting to an unknown share succeeded")
	}
	if err := conn.TreeConnect("C$"); err == nil {
		t.Error("connecting to C$ succeeded")
	}
	if err := conn.TreeConnect("other"); err != nil {
		t.Errorf("share names must ignore case: %v", err)
	}
	if got, err := readAll(conn, "other", "x"); err != nil || string(got) != "x" {
		t.Errorf("read from the second share = %q, %v", got, err)
	}
	// Files of one share are not visible through the other.
	if _, err := readAll(conn, "other", "setup.exe"); err == nil {
		t.Error("a file of another share was readable")
	}
	// There are no named pipes, so nothing can be asked over RPC.
	if err := conn.TreeConnect("IPC$"); err == nil {
		if f, err := conn.OpenFile("IPC$", "srvsvc"); err == nil {
			f.CloseFile()
			t.Error("opening the srvsvc pipe succeeded")
		}
	}
}

func TestDropClient(t *testing.T) {
	srv, port := startServer(t, Config{Shares: map[string]fs.FS{testShare: testTree()}})
	conn := connect(t, port)
	if _, err := readAll(conn, testShare, "setup.exe"); err != nil {
		t.Fatal(err)
	}
	if n := srv.DropClient(netip.MustParseAddr("192.0.2.1")); n != 0 {
		t.Errorf("dropped %d connections of an address that has none", n)
	}
	if n := srv.DropClient(netip.MustParseAddr("::ffff:127.0.0.1")); n != 1 {
		t.Errorf("dropped %d connections, want 1", n)
	}
	if _, err := readAll(conn, testShare, "setup.exe"); err == nil {
		t.Error("the connection still works after DropClient")
	}
	if n := srv.DropClient(netip.MustParseAddr("127.0.0.1")); n != 0 {
		t.Errorf("dropped %d connections on the second call", n)
	}
	// The client can come back.
	if _, err := readAll(connect(t, port), testShare, "setup.exe"); err != nil {
		t.Errorf("reconnect: %v", err)
	}
}

func TestMaxConnections(t *testing.T) {
	_, port := startServer(t, Config{Shares: map[string]fs.FS{testShare: testTree()}, MaxConnections: 2})
	connect(t, port)
	connect(t, port)
	opts := clientOptions(port)
	if conn, err := smb.NewConnection(opts); err == nil {
		conn.Close()
		t.Error("a third connection was served")
	}
}

func TestHandleLimitOverTheWire(t *testing.T) {
	srv, port := startServer(t, Config{Shares: map[string]fs.FS{testShare: testTree()}, MaxOpenHandles: 2})
	conn := connect(t, port)
	a, err := conn.OpenFile(testShare, "setup.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.OpenFile(testShare, `sources\boot.wim`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.OpenFile(testShare, `boot\bcd`); err == nil {
		t.Error("a third handle was opened")
	}
	a.CloseFile()
	if _, err := conn.OpenFile(testShare, `boot\bcd`); err != nil {
		t.Errorf("open after a close: %v", err)
	}

	// Handles of a client that goes away are released.
	conn.Close()
	waitForNoHandles(t, srv)
}

func openHandles(srv *Server) int {
	return srv.srv.Config.Shares[testShare].VFS.(*shareFS).handles.count()
}

func waitForNoHandles(t *testing.T, srv *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for openHandles(srv) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d handles still open", openHandles(srv))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLogoffClosesHandles guards against a defect of the SMB library: it
// forgets the handles of a session when the client logs off. A client could
// use that to fill the handle table for good.
func TestLogoffClosesHandles(t *testing.T) {
	srv, port := startServer(t, Config{Shares: map[string]fs.FS{testShare: testTree()}, MaxOpenHandles: 4})
	for round := 0; round < 10; round++ {
		conn, err := smb.NewConnection(clientOptions(port))
		if err != nil {
			t.Fatalf("round %d: connect: %v", round, err)
		}
		for _, p := range []string{"setup.exe", `sources\boot.wim`, `boot\bcd`} {
			if _, err := conn.OpenFile(testShare, p); err != nil {
				t.Fatalf("round %d: open %q: %v", round, p, err)
			}
		}
		if got := openHandles(srv); got != 3 {
			t.Fatalf("round %d: %d handles open, want 3", round, got)
		}
		// The client library reports an error for the reply to a logoff;
		// what counts here is what the server does with the request.
		conn.Logoff()
		waitForNoHandles(t, srv)
		conn.Close()
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	good := func() Config {
		return Config{Shares: map[string]fs.FS{testShare: testTree()}, Username: testUser, Password: testPassword}
	}
	if _, err := New(good()); err != nil {
		t.Fatalf("good config rejected: %v", err)
	}
	tests := []struct {
		name string
		edit func(*Config)
	}{
		{"no shares", func(c *Config) { c.Shares = nil }},
		{"nil file system", func(c *Config) { c.Shares = map[string]fs.FS{testShare: nil} }},
		{"share name with a space", func(c *Config) { c.Shares = map[string]fs.FS{"win 11": testTree()} }},
		{"share name with a backslash", func(c *Config) { c.Shares = map[string]fs.FS{`a\b`: testTree()} }},
		{"hidden share name", func(c *Config) { c.Shares = map[string]fs.FS{"IPC$": testTree()} }},
		{"empty share name", func(c *Config) { c.Shares = map[string]fs.FS{"": testTree()} }},
		{"share names that differ by case", func(c *Config) { c.Shares = map[string]fs.FS{"a": testTree(), "A": testTree()} }},
		{"empty user", func(c *Config) { c.Username = "" }},
		{"user with a batch metacharacter", func(c *Config) { c.Username = "kyro&calc" }},
		{"empty password", func(c *Config) { c.Password = "" }},
		{"password with a space", func(c *Config) { c.Password = "two words" }},
		{"password with a percent sign", func(c *Config) { c.Password = "100%" }},
		{"password with a quote", func(c *Config) { c.Password = `a"b` }},
		{"negative limit", func(c *Config) { c.MaxConnections = -1 }},
		{"negative timeout", func(c *Config) { c.IdleTimeout = -time.Second }},
	}
	for _, tt := range tests {
		cfg := good()
		tt.edit(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: accepted", tt.name)
		} else if strings.Contains(err.Error(), "two words") || strings.Contains(err.Error(), "100%") {
			t.Errorf("%s: the error message contains the password: %v", tt.name, err)
		}
	}
}

func TestShutdown(t *testing.T) {
	srv, err := New(Config{Shares: map[string]fs.FS{testShare: testTree()}, Username: testUser, Password: testPassword})
	if err != nil {
		t.Fatal(err)
	}
	l, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	if err := <-done; !errors.Is(err, ErrServerClosed) {
		t.Errorf("Serve returned %v", err)
	}
	if _, err := net.DialTimeout("tcp", l.Addr().String(), time.Second); err == nil {
		t.Error("the port still accepts connections")
	}
}
