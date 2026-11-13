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
	"log/slog"

	"github.com/kyro-pxe/kyro/internal/server"
)

func (e *env) serve(args []string) error {
	c := e.command("serve", "[flags]", `Run the server: proxyDHCP, TFTP, HTTP and the install share, as the
configuration says. It runs until it is stopped.
`)
	debug := c.Bool("debug", false, "log every request, including those of the SMB library")
	pos, err := c.parse(args)
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
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug - 4
	}
	log := slog.New(slog.NewTextHandler(e.stderr, &slog.HandlerOptions{Level: level}))
	log.Info("kyro starting", "version", version, "interface", cfg.Interface, "address", cfg.Address, "data_dir", cfg.DataDir)
	err = server.Run(e.ctx, cfg, log, server.Options{})
	if err == nil {
		log.Info("kyro stopped")
	}
	return err
}
