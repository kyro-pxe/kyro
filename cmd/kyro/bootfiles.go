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

	"github.com/kyro-pxe/kyro/internal/bootfiles"
)

func (e *env) bootfiles(args []string) error {
	if len(args) == 0 {
		return usageErr("use 'kyro bootfiles fetch' or 'kyro bootfiles verify'")
	}
	switch args[0] {
	case "fetch":
		c := e.command("bootfiles fetch", "[flags]", `Download iPXE and wimboot from their projects' release pages and install
them into the data directory. The versions are fixed (`+bootfiles.Versions+`),
and so are their SHA-256 checksums: a file that does not match is not
installed.

Both programs are licensed under the GNU GPL, version 2. Kyro uses them as
the unmodified binaries that their projects release.
`)
		from := c.String("from", "", "take wimboot and ipxeboot.tar.gz from this directory instead of the network")
		pos, err := c.parse(args[1:])
		if err != nil {
			return err
		}
		if len(pos) > 0 {
			return usageErr("unexpected argument %q", pos[0])
		}
		cfg, err := c.config()
		if err != nil {
			return err
		}
		return bootfiles.Fetch(e.ctx, cfg.BootDir(), bootfiles.Files, bootfiles.Options{
			FromDir: *from,
			Report:  func(format string, a ...any) { fmt.Fprintf(e.stdout, format+"\n", a...) },
		})
	case "verify":
		c := e.command("bootfiles verify", "", "Check the installed iPXE and wimboot files against the pinned checksums.\n")
		if _, err := c.parse(args[1:]); err != nil {
			return err
		}
		cfg, err := c.config()
		if err != nil {
			return err
		}
		problems := bootfiles.Verify(cfg.BootDir(), bootfiles.Files)
		for _, p := range problems {
			if errors.Is(p.Err, bootfiles.ErrMissing) {
				fmt.Fprintf(e.stdout, "%s is missing\n", p.File)
			} else {
				fmt.Fprintf(e.stdout, "%s: %v\n", p.File, p.Err)
			}
		}
		if len(problems) > 0 {
			fmt.Fprintln(e.stdout, "Run 'kyro bootfiles fetch'.")
			return errReported
		}
		fmt.Fprintf(e.stdout, "The boot files in %s are present and have the pinned checksums (%s).\n", cfg.BootDir(), bootfiles.Versions)
		return nil
	}
	return usageErr("unknown subcommand %q; use fetch or verify", args[0])
}
