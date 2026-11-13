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
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"

	"github.com/kyro-pxe/kyro/internal/smbshare"
)

const shareUsage = `Serve a directory as a read-only SMB share.

Experimental. This is the install share on its own, for testing it against
Windows PE. It is not the PXE server: nothing boots a client or tells it
where the share is. Put the content of a Windows ISO into the directory, for
example by mounting the ISO there.

The account is not a secret. Kyro will write it into a script that every
client on the boot network can read. Anyone who can reach the port can read
everything in the directory, so listen on the boot network only.

Usage:
  kyro share -listen ADDRESS:PORT -root DIRECTORY -password PASSWORD [flags]

Flags:
`

// runShare implements "kyro share". It serves until ctx is done.
func runShare(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("kyro share", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprint(stderr, shareUsage)
		flags.PrintDefaults()
	}
	listen := flags.String("listen", "", "address and port to listen on, for example 192.168.77.1:445 (required)")
	root := flags.String("root", "", "directory to serve (required)")
	name := flags.String("name", "install", "name of the share")
	user := flags.String("user", "kyro", "account name")
	password := flags.String("password", "", "account password (required)")
	debug := flags.Bool("debug", false, "log every request")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "kyro share: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	if *listen == "" || *root == "" || *password == "" {
		fmt.Fprintln(stderr, "kyro share: -listen, -root and -password are required")
		return 2
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug - 4
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	// os.Root refuses every path that leaves the directory, whatever the
	// share is asked for.
	dir, err := os.OpenRoot(*root)
	if err != nil {
		fmt.Fprintf(stderr, "kyro share: %v\n", err)
		return 1
	}
	defer dir.Close()

	srv, err := smbshare.New(smbshare.Config{
		Shares:   map[string]fs.FS{*name: dir.FS()},
		Username: *user,
		Password: *password,
		Logger:   log,
	})
	if err != nil {
		fmt.Fprintf(stderr, "kyro share: %v\n", err)
		return 2
	}
	l, err := smbshare.Listen(*listen)
	if err != nil {
		fmt.Fprintf(stderr, "kyro share: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "serving %s read-only as \\\\%s\\%s\n", *root, l.Addr(), *name)

	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	select {
	case <-ctx.Done():
		srv.Close()
		<-done
		return 0
	case err := <-done:
		fmt.Fprintf(stderr, "kyro share: %v\n", err)
		return 1
	}
}
