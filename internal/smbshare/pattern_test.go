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
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		pattern, name string
		want          bool
	}{
		// No wildcards: the name itself, in any case.
		{"install.wim", "install.wim", true},
		{"INSTALL.WIM", "install.wim", true},
		{"ei.cfg", "EI.CFG", true},
		{"install.wim", "install.esd", false},
		{"install", "install.wim", false},
		{"straße", "STRASSE", false}, // simple case folding only
		{"é.txt", "É.TXT", true},

		// Star and question mark.
		{"", "anything", true},
		{"*", "anything", true},
		{"*", "", true},
		{"*.wim", "install.wim", true},
		{"*.wim", "install.esd", false},
		{"inst*", "install.wim", true},
		{"*st*", "install.wim", true},
		{"*.*", "install.wim", true},
		{"*.*", "noextension", false},
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "aXXbYY", false},
		{"????", "boot", true},
		{"????", "boo", false},
		{"????", "boots", false},
		{"boot.???", "boot.wim", true},
		{"boot.???", "boot.wi", false},

		// What Windows sends for "dir install.*": the dot becomes DOS_DOT.
		{`install"*`, "install.wim", true},
		{`install"*`, "install.esd", true},
		{`install"*`, "INSTALL.WIM", true},
		{`install"*`, "install", true},
		{`install"*`, "installer.exe", false},
		{`install"*`, "install_x.wim", false},

		// What Windows sends for "dir *.": names without an extension.
		{`<`, "sources", true},
		{`<"`, "sources", true},
		{`<"`, "boot.", true},

		// What Windows sends for "dir boot.???" and "dir ????.wim".
		{`boot">>>`, "boot.wim", true},
		{`boot">>>`, "boot.w", true},
		{`boot">>>`, "boot", true},
		{`boot">>>`, "boot.wims", false},
		{`>>>>"wim`, "boot.wim", true},
		{`>>>>"wim`, "bo.wim", true},
		{`>>>>"wim`, "boots.wim", false},

		// DOS_STAR stops at the last dot.
		{`<.wim`, "install.wim", true},
		{`<.wim`, "a.b.wim", true},
		{`<.b.wim`, "a.b.wim", true},
		{`<x`, "a.bx", false},
		{`*x`, "a.bx", true},
		{`<wim`, "install.wim", true},
		{`<`, "a.b.c", false},
		{`<c`, "a.b.c", true},

		// The directory entries for the directory itself and its parent.
		{"*", ".", true},
		{"*", "..", true},
		{`install"*`, ".", false},
		{`install"*`, "..", false},
	}
	for _, tt := range tests {
		if got := matchPattern(tt.pattern, tt.name); got != tt.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", tt.pattern, tt.name, got, tt.want)
		}
		if ref := matchReference([]rune(tt.pattern), []rune(tt.name)); tt.pattern != "" && ref != tt.want {
			t.Errorf("reference(%q, %q) = %v, want %v", tt.pattern, tt.name, ref, tt.want)
		}
	}
}

func TestMatchPatternTooLong(t *testing.T) {
	long := strings.Repeat("*", maxPatternRunes+1)
	if matchPattern(long, "x") {
		t.Error("a pattern over the limit matched")
	}
	if !matchPattern(strings.Repeat("*", maxPatternRunes), "x") {
		t.Error("a pattern at the limit did not match")
	}
}

// matchReference is a second implementation of the matching rules, written
// as the plain recursion of MS-FSA 2.1.4.4 without any attempt at speed.
// The fuzz test compares the two.
func matchReference(p, n []rune) bool {
	lastDot := -1
	for i, r := range n {
		if r == '.' {
			lastDot = i
		}
	}
	var rec func(pi, ni int) bool
	rec = func(pi, ni int) bool {
		if pi == len(p) {
			return ni == len(n)
		}
		switch p[pi] {
		case '*':
			for j := ni; j <= len(n); j++ {
				if rec(pi+1, j) {
					return true
				}
			}
			return false
		case '?':
			return ni < len(n) && rec(pi+1, ni+1)
		case '<':
			for j := ni; j <= len(n); j++ {
				if rec(pi+1, j) {
					return true
				}
				if j == lastDot {
					return rec(pi+1, j+1)
				}
			}
			return false
		case '>':
			if ni == len(n) {
				return rec(pi+1, ni)
			}
			if n[ni] == '.' {
				if ni+1 == len(n) && rec(pi+1, ni+1) {
					return true
				}
				return rec(pi+1, ni)
			}
			return rec(pi+1, ni+1)
		case '"':
			if ni == len(n) {
				return rec(pi+1, ni)
			}
			return n[ni] == '.' && rec(pi+1, ni+1)
		default:
			return ni < len(n) && equalFold(p[pi], n[ni]) && rec(pi+1, ni+1)
		}
	}
	return rec(0, 0)
}

func FuzzMatchPattern(f *testing.F) {
	for _, s := range [][2]string{
		{"*", "install.wim"},
		{`install"*`, "install.wim"},
		{`boot">>>`, "boot.wim"},
		{`<.wim`, "a.b.wim"},
		{"a*b*c", "aXXbYYc"},
		{"????", "boot"},
		{`<"`, "sources"},
		{`>>>>"wim`, "bo.wim"},
	} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, pattern, name string) {
		// The reference is exponential; keep its inputs small.
		if !utf8.ValidString(pattern) || !utf8.ValidString(name) ||
			utf8.RuneCountInString(pattern) > 12 || utf8.RuneCountInString(name) > 16 {
			matchPattern(pattern, name) // must still not panic
			return
		}
		if pattern == "" {
			return // means "everything" by convention, not by the rules
		}
		got := matchPattern(pattern, name)
		want := matchReference([]rune(pattern), []rune(name))
		if got != want {
			t.Errorf("matchPattern(%q, %q) = %v, reference says %v", pattern, name, got, want)
		}
	})
}
