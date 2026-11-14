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

// Command kyro is the Kyro PXE server and its management tool.
//
// The project is an alpha. See docs/ROADMAP.md for what works.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/kyro-pxe/kyro/internal/config"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `Kyro - open-source PXE server for installing Windows 11 over the network (alpha)

Usage:
  kyro <command> [arguments]

Server:
  serve             Run the server
  doctor            Check whether this machine is ready to run the server

Images and profiles:
  image add|list|remove       Windows ISOs to install from
  profile import|list|show|remove
                              Answer files to install with
  bootfiles fetch|verify      iPXE and wimboot, from their release pages

Clients:
  arm <client> --image X --profile Y
                    Install a client on its next network boot, once
  disarm <client>   Take that back
  clients           List the clients and their state

Other:
  share             Serve a directory as a read-only SMB share (for testing)
  version           Print the version
  help              Show this help

A client is named by its MAC address, its SMBIOS UUID or its serial number.
That selects a machine; it does not authenticate it.

Commands read /etc/kyro/config.yaml, or the file named by --config or by the
environment variable KYRO_CONFIG. 'kyro <command> -h' shows the flags of a
command.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// env is what a command gets to work with.
type env struct {
	ctx    context.Context
	stdout io.Writer
	stderr io.Writer
}

// errUsage marks an error as a mistake in how the command was called. It
// leads to exit code 2 instead of 1.
type errUsage struct{ msg string }

func (e errUsage) Error() string { return e.msg }

func usageErr(format string, args ...any) error {
	return errUsage{fmt.Sprintf(format, args...)}
}

// run executes the CLI and returns the process exit code. Commands that
// keep running stop when ctx is done.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	e := &env{ctx: ctx, stdout: stdout, stderr: stderr}
	var err error
	name, rest := args[0], args[1:]
	switch name {
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "kyro %s\n", version)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	case "share":
		return runShare(ctx, rest, stdout, stderr)
	case "serve":
		err = e.serve(rest)
	case "image":
		err = e.image(rest)
	case "profile":
		err = e.profile(rest)
	case "bootfiles":
		err = e.bootfiles(rest)
	case "arm":
		err = e.arm(rest)
	case "disarm":
		err = e.disarm(rest)
	case "clients":
		err = e.clients(rest)
	case "doctor":
		err = e.doctor(rest)
	default:
		fmt.Fprintf(stderr, "kyro: unknown command %q\n\n", name)
		fmt.Fprint(stderr, usage)
		return 2
	}
	var u errUsage
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errFlag):
		return 2 // the flag package has printed the message
	case errors.As(err, &u):
		fmt.Fprintf(stderr, "kyro %s: %s\n", name, u.msg)
		return 2
	case errors.Is(err, errReported):
		return 1
	}
	fmt.Fprintf(stderr, "kyro %s: %v\n", name, err)
	return 1
}

var (
	// errFlag is returned when the flag package rejected the arguments.
	errFlag = errors.New("bad flags")
	// errReported is returned by a command that has printed what went
	// wrong itself.
	errReported = errors.New("failed")
)

// command is a flag set for one command, with the --config flag that all
// of them share.
type command struct {
	*flag.FlagSet
	configPath *string
}

func (e *env) command(name, synopsis, help string) *command {
	fs := flag.NewFlagSet("kyro "+name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	c := &command{FlagSet: fs}
	def := os.Getenv("KYRO_CONFIG")
	if def == "" {
		def = config.DefaultPath
	}
	c.configPath = fs.String("config", def, "configuration file")
	fs.Usage = func() {
		fmt.Fprintf(e.stderr, "Usage:\n  kyro %s %s\n\n%s\nFlags:\n", name, synopsis, help)
		fs.PrintDefaults()
	}
	return c
}

// parse reads flags and positional arguments in any order, so that
// "kyro arm 52:54:00:12:34:56 --image win11" works as well as the reverse.
func (c *command) parse(args []string) ([]string, error) {
	var positional []string
	for {
		if err := c.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, errFlag
		}
		args = c.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

// config loads the configuration that the command was pointed at.
func (c *command) config() (config.Config, error) {
	cfg, err := config.Load(*c.configPath)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, fmt.Errorf("there is no configuration at %s; install.sh writes one, or name another with --config", *c.configPath)
	}
	return cfg, err
}
