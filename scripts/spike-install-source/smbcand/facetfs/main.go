// Copyright (C) 2026 Lukas Koch and Kyro contributors
// SPDX-License-Identifier: GPL-3.0-or-later

// Spike only: serves a directory read-only over SMB with one candidate
// library, so that Windows PE can be tested against it. Not part of Kyro.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"strings"

	"github.com/sirrobot01/facetfs"
	"github.com/sirrobot01/facetfs/smb"
)

type oneUser struct {
	user string
	hash []byte
}

func (a oneUser) NTHash(_ context.Context, _, user string) ([]byte, error) {
	if !strings.EqualFold(user, a.user) {
		return nil, errors.New("unknown user")
	}
	return append([]byte(nil), a.hash...), nil
}

func main() {
	addr := flag.String("addr", "192.168.77.1:445", "listen address")
	root := flag.String("root", "/var/lib/kyro-spike/mnt", "directory to serve")
	share := flag.String("share", "win11", "share name")
	user := flag.String("user", "kyro", "account name")
	pass := flag.String("pass", "kyro-spike", "account password")
	flag.Parse()

	served, err := facetfs.OpenDir(*root)
	if err != nil {
		log.Fatal(err)
	}
	defer served.Close()
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	srv := &smb.Server{
		FileSystem:     served,
		Authenticator:  oneUser{user: *user, hash: smb.NTHash(*pass)},
		ShareName:      *share,
		RequireSigning: true,
		Logger:         func(err error) { log.Printf("connection: %v", err) },
	}
	log.Fatal(srv.Serve(context.Background(), listener))
}
