// Copyright (C) 2026 Lukas Koch and Kyro contributors
// SPDX-License-Identifier: GPL-3.0-or-later

// Spike only: serves a directory read-only over SMB with one candidate
// library, so that Windows PE can be tested against it. Not part of Kyro.
package main

import (
	"flag"
	"log"

	"github.com/jfjallid/go-smb/ntlmssp"
	"github.com/jfjallid/go-smb/smb"
	"github.com/jfjallid/go-smb/smb/server"
	"github.com/jfjallid/go-smb/smb/server/filevfs"
)

func main() {
	addr := flag.String("addr", "192.168.77.1:445", "listen address")
	root := flag.String("root", "/var/lib/kyro-spike/mnt", "directory to serve")
	share := flag.String("share", "win11", "share name")
	user := flag.String("user", "kyro", "account name")
	pass := flag.String("pass", "kyro-spike", "account password")
	flag.Parse()

	fs, err := filevfs.New(filevfs.Options{Root: *root, ReadOnly: true})
	if err != nil {
		log.Fatal(err)
	}
	srv := &server.Server{Config: &server.ServerConfig{
		NetBIOSName:     "KYRO",
		SigningRequired: true,
		Authenticator: &server.MapAuthenticator{
			Accounts: map[string]*server.Account{*user: {NTHash: ntlmssp.Ntowfv1(*pass)}},
		},
		Shares: map[string]server.Share{
			*share: {Name: *share, Type: smb.ShareTypeDisk, VFS: fs},
		},
	}}
	log.Fatal(srv.ListenAndServe(*addr))
}
