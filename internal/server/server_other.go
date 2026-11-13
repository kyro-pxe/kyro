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

//go:build !linux

// Package server runs all of Kyro's services together. It needs Linux.
package server

import (
	"context"
	"errors"
	"log/slog"

	"github.com/kyro-pxe/kyro/internal/bootfiles"
	"github.com/kyro-pxe/kyro/internal/config"
)

// Options changes how Run behaves.
type Options struct {
	BootFiles          []bootfiles.File
	SkipInterfaceCheck bool
	Ready              func()
}

// Run fails: the service runs on Linux only.
func Run(context.Context, config.Config, *slog.Logger, Options) error {
	return errors.New("kyro serve runs on Linux only")
}
