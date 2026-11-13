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

// Command kyro is the entry point of the Kyro PXE server.
//
// The project is in early development. It cannot boot anything yet.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `Kyro - open-source PXE server for booting Windows 11 ISOs (early development)

Usage:
  kyro <command>

Commands:
  serve      Start the PXE server (not implemented yet)
  share      Serve a directory as a read-only SMB share (experimental)
  version    Print the version
  help       Show this help
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run executes the CLI and returns the process exit code. Commands that
// keep running stop when ctx is done.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}

	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "kyro %s\n", version)
		return 0
	case "serve":
		fmt.Fprintln(stderr, "kyro serve: not implemented yet")
		return 1
	case "share":
		return runShare(ctx, args[1:], stdout, stderr)
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "kyro: unknown command %q\n\n", args[0])
		fmt.Fprint(stderr, usage)
		return 2
	}
}
