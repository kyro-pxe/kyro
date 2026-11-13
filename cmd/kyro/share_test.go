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

package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jfjallid/go-smb/smb"
	"github.com/jfjallid/go-smb/spnego"
)

// TestShareServesDirectory runs "kyro share" on a loopback port, reads a
// file with an SMB client and stops the command.
func TestShareServesDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sources"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sources", "install.wim"), []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outR, outW := io.Pipe()
	var stderr bytes.Buffer
	code := make(chan int, 1)
	go func() {
		code <- run(ctx, []string{"share", "-listen", "127.0.0.1:0", "-root", dir, "-password", "test-only", "-name", "win11"}, outW, &stderr)
		outW.Close()
	}()

	// The first line of output names the address.
	line, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil {
		t.Fatalf("no output: %v; stderr: %s", err, stderr.String())
	}
	go io.Copy(io.Discard, outR)
	if !strings.Contains(line, "read-only") || !strings.HasSuffix(line, "\\win11\n") {
		t.Errorf("output = %q", line)
	}
	addr := line[strings.Index(line, `\\`)+2 : strings.LastIndex(line, `\`)]
	_, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("cannot find the address in %q: %v", line, err)
	}
	port, _ := strconv.Atoi(portText)

	conn, err := smb.NewConnection(smb.Options{
		Host:                  "127.0.0.1",
		Port:                  port,
		Initiator:             &spnego.NTLMInitiator{User: "kyro", Password: "test-only"},
		RequireMessageSigning: true,
		DialTimeout:           5 * time.Second,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()
	var got bytes.Buffer
	if err := conn.RetrieveFile("win11", `SOURCES\INSTALL.WIM`, 0, got.Write); err != nil || got.String() != "image" {
		t.Errorf("read = %q, %v", got.String(), err)
	}

	cancel()
	select {
	case c := <-code:
		if c != 0 {
			t.Errorf("exit code %d; stderr: %s", c, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the command did not stop")
	}
}
