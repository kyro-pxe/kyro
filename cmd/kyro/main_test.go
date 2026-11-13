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
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"version", []string{"version"}, 0, "kyro " + version, ""},
		{"serve is a stub", []string{"serve"}, 1, "", "not implemented yet"},
		{"help", []string{"help"}, 0, "Usage:", ""},
		{"help lists share", []string{"help"}, 0, "share ", ""},
		{"share without flags", []string{"share"}, 2, "", "-listen, -root and -password are required"},
		{"share with an unknown flag", []string{"share", "-bogus"}, 2, "", "flag provided but not defined"},
		{"share with an extra argument", []string{"share", "-listen", "127.0.0.1:0", "-root", ".", "-password", "x", "extra"}, 2, "", "unexpected argument"},
		{"share with a missing directory", []string{"share", "-listen", "127.0.0.1:0", "-root", "/nonexistent/kyro", "-password", "x"}, 1, "", "no such file"},
		{"share with a bad password", []string{"share", "-listen", "127.0.0.1:0", "-root", ".", "-password", "a b"}, 2, "", "invalid password"},
		{"share with a bad share name", []string{"share", "-listen", "127.0.0.1:0", "-root", ".", "-password", "x", "-name", "a b"}, 2, "", "invalid share name"},
		{"share with a bad address", []string{"share", "-listen", "nonsense", "-root", ".", "-password", "x"}, 1, "", "kyro share:"},
		{"share help", []string{"share", "-h"}, 0, "", "Experimental"},
		{"no command", nil, 2, "", "Usage:"},
		{"unknown command", []string{"bogus"}, 2, "", `unknown command "bogus"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			if code := run(context.Background(), tt.args, &stdout, &stderr); code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}
