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
	"fmt"

	"github.com/kyro-pxe/kyro/internal/config"
	"github.com/kyro-pxe/kyro/internal/doctor"
)

func (e *env) doctor(args []string) error {
	c := e.command("doctor", "", `Check whether this machine is ready to run Kyro: the configuration, the
boot interface, the ports Kyro needs, services and firewalls that get in the
way, the boot files and the data directory. Run it as root; an ordinary
user cannot check ports below 1024.

It only looks. Where something has to change, it prints the commands and
leaves running them to you.
`)
	pos, err := c.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageErr("unexpected argument %q", pos[0])
	}
	cfg, cfgErr := config.Load(*c.configPath)
	warnings, failures := doctor.Print(e.stdout, doctor.Check(doctor.Host(), *c.configPath, cfg, cfgErr))
	fmt.Fprintln(e.stdout)
	switch {
	case failures > 0:
		fmt.Fprintf(e.stdout, "%d problems that keep Kyro from working, %d warnings.\n", failures, warnings)
		return errReported
	case warnings > 0:
		fmt.Fprintf(e.stdout, "No problems that keep Kyro from working. %d warnings to look at.\n", warnings)
	default:
		fmt.Fprintln(e.stdout, "Everything Kyro checks looks right.")
	}
	return nil
}
