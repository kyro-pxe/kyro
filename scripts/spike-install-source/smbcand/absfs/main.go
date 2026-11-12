// Copyright (C) 2026 Lukas Koch and Kyro contributors
// SPDX-License-Identifier: GPL-3.0-or-later

// Spike only: serves a directory read-only over SMB with one candidate
// library, so that Windows PE can be tested against it. Not part of Kyro.
package main

import (
	"flag"
	"log"
	"net"
	"strconv"

	"github.com/absfs/osfs"
	"github.com/absfs/smbfs"
)

func main() {
	addr := flag.String("addr", "192.168.77.1:445", "listen address")
	root := flag.String("root", "/var/lib/kyro-spike/mnt", "directory to serve")
	share := flag.String("share", "win11", "share name")
	user := flag.String("user", "kyro", "account name")
	pass := flag.String("pass", "kyro-spike", "account password")
	flag.Parse()

	host, portText, err := net.SplitHostPort(*addr)
	if err != nil {
		log.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		log.Fatal(err)
	}
	fs, err := osfs.NewFS()
	if err != nil {
		log.Fatal(err)
	}
	srv, err := smbfs.NewServer(smbfs.ServerOptions{
		Port:            port,
		Hostname:        host,
		ServerName:      "KYRO",
		SigningRequired: true,
		AllowGuest:      false,
		Debug:           true,
		Users:           map[string]string{*user: *pass},
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := srv.AddShare(fs, smbfs.ShareOptions{ShareName: *share, SharePath: *root, ReadOnly: true}); err != nil {
		log.Fatal(err)
	}
	log.Fatal(srv.ListenAndServe())
}
