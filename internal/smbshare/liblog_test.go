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

package smbshare

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRateLimit(t *testing.T) {
	r := &rateLimit{max: 2, interval: time.Minute}
	t0 := time.Unix(1_000_000, 0)
	steps := []struct {
		at         time.Duration
		ok         bool
		suppressed int
	}{
		{0, true, 0},
		{time.Second, true, 0},
		{2 * time.Second, false, 0},
		{59 * time.Second, false, 0},
		{60 * time.Second, true, 2}, // new interval: reports what was held back
		{61 * time.Second, true, 0},
		{62 * time.Second, false, 0},
		{10 * time.Minute, true, 1},
	}
	for i, s := range steps {
		ok, suppressed := r.allow(t0.Add(s.at))
		if ok != s.ok || suppressed != s.suppressed {
			t.Errorf("step %d: allow = %v, %d; want %v, %d", i, ok, suppressed, s.ok, s.suppressed)
		}
	}
}

func TestLibraryLoggerLevels(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	l := libraryLogger{log: log, panics: &rateLimit{max: 1, interval: time.Hour}}

	// What the library calls an error is usually a malformed request. A
	// client can send as many as it likes, so they stay below info level.
	l.Errorf("Create: decode CreateReq: %v", "short")
	l.Errorln("signPDU: no signer configured")
	l.Noticef("accepting on %s", "127.0.0.1:445")
	l.Infof("info")
	l.Debugf("debug")
	if buf.Len() != 0 {
		t.Errorf("client noise was logged at info level or above: %s", buf.String())
	}

	// A recovered panic is logged as an error, with the stack, once.
	l.Errorf("recovered from panic while serving connection: %v", "boom")
	l.Errorf("recovered from panic while serving connection: %v", "boom")
	out := buf.String()
	if strings.Count(out, "level=ERROR") != 1 || !strings.Contains(out, "boom") || !strings.Contains(out, "stack=") {
		t.Errorf("panic report = %q", out)
	}
}

func TestLibraryLoggerQuotesClientText(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: levelTrace}))
	libraryLogger{log: log}.Debugf("Create: path=%s", "x\nlevel=ERROR msg=forged\x1b[2J")
	out := buf.String()
	if strings.Count(out, "\n") != 1 || strings.Contains(out, "\x1b") {
		t.Errorf("client text reached the log unquoted: %q", out)
	}
}
