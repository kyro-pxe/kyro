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
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/kyro-pxe/kyro/internal/drivers"
)

func (e *env) driver(args []string) error {
	if len(args) == 0 {
		return usageErr("use 'kyro driver add <directory>', 'fetch [name]', 'list' or 'remove <name>'")
	}
	switch args[0] {
	case "add":
		return e.driverAdd(args[1:])
	case "fetch":
		return e.driverFetch(args[1:])
	case "list", "ls":
		return e.driverList(args[1:])
	case "remove", "rm":
		return e.driverRemove(args[1:])
	}
	return usageErr("unknown subcommand %q; use add, fetch, list or remove", args[0])
}

// driverNotice is printed whenever a pack is added. Kyro cannot judge a
// driver.
const driverNotice = `Every installation from now on is given this pack. A driver is code that
runs with full rights on every machine it is loaded on, in Windows PE and
in the installed Windows. Kyro copies the files as they are and does not
check where they come from. Take drivers from the maker of the hardware.`

// packName makes a name for a pack out of the name of its directory.
func packName(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, c := range strings.ToLower(filepath.Base(abs)) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	name := strings.TrimSuffix(b.String(), "-")
	if len(name) > 32 {
		name = strings.TrimSuffix(name[:32], "-")
	}
	return name
}

func (e *env) driverAdd(args []string) error {
	c := e.command("driver add", "<directory> [flags]", `Store a driver pack: a directory with the .inf files of a driver and the
files they name, as the maker of the hardware ships it. If the maker ships
an installer, unpack it first.

Add what Windows PE lacks for the network card and the disk controller of
your machines. Without the first, the installation stops when it connects
to the install share; without the second, Setup finds no disk.

Every installation is given all packs. Windows PE loads each driver; one
that matches no device of the machine does nothing. The packs also stay
where Windows Setup looks for drivers to add to the system it installs.
All packs together are limited to 100 MB, because Windows PE holds them in
memory.
`)
	name := c.String("name", "", "name of the pack (default: from the name of the directory)")
	replace := c.Bool("replace", false, "overwrite a pack of the same name")
	pos, err := c.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("name one directory")
	}
	if *name == "" {
		*name = packName(pos[0])
	}
	if !drivers.ValidName(*name) {
		return usageErr("%q cannot be the name of a pack; give one with --name (lower-case letters, digits and hyphens, at most 32 characters)", *name)
	}
	cfg, err := c.config()
	if err != nil {
		return err
	}
	p, err := drivers.Open(cfg.DriversDir()).Add(pos[0], *name, *replace)
	if errors.Is(err, drivers.ErrExists) {
		return fmt.Errorf("there is a driver pack called %q already; use --replace to overwrite it, or --name for another name", *name)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Driver pack %s added: %d files, %s.\n", p.Name, len(p.Files), size(p.Size()))
	fmt.Fprintf(e.stdout, "Drivers that Windows PE will load (%d):\n", len(p.INFs()))
	for _, inf := range p.INFs() {
		fmt.Fprintf(e.stdout, "  %s\n", inf)
	}
	fmt.Fprintf(e.stdout, "\n%s\n", driverNotice)
	return nil
}

func (e *env) driverFetch(args []string) error {
	c := e.command("driver fetch", "[name] [flags]", `Download a driver pack from its maker and store it. Without a name, list
what can be fetched.

The versions are fixed, and so are the SHA-256 checksums of every file: a
download that does not match is not stored. Kyro contains none of these
files and passes none on; it downloads them to this machine for you.

Some makers let nobody download their drivers without accepting a license.
For those, read the license at the address shown and say that you accept
it with --accept-license.
`)
	accept := c.Bool("accept-license", false, "you have read and accept the license under which the maker offers the drivers")
	from := c.String("from", "", "take the download from this file instead of the network")
	pos, err := c.parse(args)
	if err != nil {
		return err
	}
	cfg, err := c.config()
	if err != nil {
		return err
	}
	store := drivers.Open(cfg.DriversDir())
	if len(pos) == 0 {
		have := map[string]drivers.Pack{}
		if packs, err := store.List(); err == nil {
			for _, p := range packs {
				have[p.Name] = p
			}
		}
		fmt.Fprintln(e.stdout, "Driver packs that 'kyro driver fetch <name>' can download:")
		for _, src := range drivers.Sources {
			fmt.Fprintf(e.stdout, "\n%s: %s, version %s\n", src.Name, src.Title, src.Version)
			fmt.Fprintf(e.stdout, "  For:     %s\n", src.Covers)
			fmt.Fprintf(e.stdout, "  From:    %s\n", src.Page)
			license := src.License
			if src.MustAccept {
				license += " (must be accepted: --accept-license)"
			}
			fmt.Fprintf(e.stdout, "  License: %s\n", license)
			switch p, ok := have[src.Name]; {
			case ok && p.Version == src.Version:
				fmt.Fprintln(e.stdout, "  Stored:  yes")
			case ok:
				fmt.Fprintln(e.stdout, "  Stored:  a pack of this name that is not this version")
			default:
				fmt.Fprintln(e.stdout, "  Stored:  no")
			}
		}
		return nil
	}
	if len(pos) != 1 {
		return usageErr("name one driver pack; 'kyro driver fetch' lists them")
	}
	src, ok := drivers.FindSource(pos[0])
	if !ok {
		return usageErr("Kyro does not know where to get %q; 'kyro driver fetch' lists what it can fetch, and 'kyro driver add' stores drivers that you have", pos[0])
	}
	p, err := store.Fetch(e.ctx, src, drivers.FetchOptions{
		AcceptLicense: *accept,
		FromFile:      *from,
		Report:        func(format string, a ...any) { fmt.Fprintf(e.stdout, format+"\n", a...) },
	})
	if errors.Is(err, drivers.ErrLicense) {
		fmt.Fprintf(e.stderr, `Nothing was downloaded. %s lets nobody download these drivers without
accepting the %s. Read it at
  %s
If you accept it, run:
  kyro driver fetch %s --accept-license
`, src.Maker, src.License, src.Page, src.Name)
		return errReported
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "License: %s, see %s\n", src.License, src.Page)
	fmt.Fprintf(e.stdout, "Drivers that Windows PE will load (%d):\n", len(p.INFs()))
	for _, inf := range p.INFs() {
		fmt.Fprintf(e.stdout, "  %s\n", inf)
	}
	fmt.Fprintln(e.stdout, "\nEvery installation from now on is given this pack. 'kyro driver remove "+p.Name+"' takes it out again.")
	return nil
}

func (e *env) driverList(args []string) error {
	c := e.command("driver list", "", "List the driver packs.\n")
	if _, err := c.parse(args); err != nil {
		return err
	}
	cfg, err := c.config()
	if err != nil {
		return err
	}
	list, err := drivers.Open(cfg.DriversDir()).List()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(e.stdout, "No driver packs. Installations use the drivers that come with Windows. Add a pack with 'kyro driver add <directory>', or see 'kyro driver fetch' for packs that Kyro can download.")
		return nil
	}
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tVERSION\tDRIVERS\tFILES\tSIZE\tADDED\tFROM")
	var total int64
	for _, p := range list {
		total += p.Size()
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\t%s\t%s\n", p.Name, dash(p.Version), len(p.INFs()), len(p.Files), size(p.Size()), p.AddedAt.Format("2006-01-02"), p.Source)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "\nEvery installation is given all of them: %s of at most %d MB.\n", size(total), drivers.MaxTotal>>20)
	return nil
}

func (e *env) driverRemove(args []string) error {
	c := e.command("driver remove", "<name>", "Remove a driver pack.\n")
	pos, err := c.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("name one driver pack")
	}
	cfg, err := c.config()
	if err != nil {
		return err
	}
	if err := drivers.Open(cfg.DriversDir()).Remove(pos[0]); errors.Is(err, drivers.ErrNotFound) {
		return fmt.Errorf("there is no driver pack called %q", pos[0])
	} else if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Driver pack %s removed.\n", pos[0])
	return nil
}
