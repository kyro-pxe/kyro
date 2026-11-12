// Copyright (C) 2026 Lukas Koch and Kyro contributors
// SPDX-License-Identifier: GPL-3.0-or-later

// Spike only: serves a directory read-only over SMB with one candidate
// library, so that Windows PE can be tested against it. Not part of Kyro.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"strings"

	"github.com/sonroyaalmerol/go-smb-server/smb/ntlmssp"
	"github.com/sonroyaalmerol/go-smb-server/smb/server"
	"github.com/sonroyaalmerol/go-smb-server/smb/vfs"
)

// anyDomain accepts one account regardless of the domain name the client
// sends. Windows PE sends its own computer name as the domain.
type anyDomain struct{ user, pass string }

func (a anyDomain) LookupNTOWFv2(_ context.Context, domain, user string) ([]byte, error) {
	if !strings.EqualFold(user, a.user) {
		return nil, ntlmssp.ErrUnknownUser
	}
	return ntlmssp.NTOWFv2(a.pass, user, domain), nil
}

func main() {
	addr := flag.String("addr", "192.168.77.1:445", "listen address")
	root := flag.String("root", "/var/lib/kyro-spike/mnt", "directory to serve")
	share := flag.String("share", "win11", "share name")
	user := flag.String("user", "kyro", "account name")
	pass := flag.String("pass", "kyro-spike", "account password")
	flag.Parse()

	backend, err := vfs.NewLocalBackend(*root)
	if err != nil {
		slog.Error("open backend", "err", err)
		os.Exit(1)
	}
	creds := anyDomain{user: *user, pass: *pass}
	srv, err := server.New(
		server.WithAddr(*addr),
		server.WithShares(vfs.NewDiskShare(*share, backend)),
		server.WithAuth(ntlmssp.NewServer(creds, "KYRO")),
		server.WithLogger(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))),
	)
	if err != nil {
		slog.Error("new server", "err", err)
		os.Exit(1)
	}
	if err := srv.ListenAndServe(context.Background()); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
