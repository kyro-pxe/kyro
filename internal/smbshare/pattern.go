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
	"unicode"
	"unicode/utf8"
)

// maxPatternRunes bounds the length of a search pattern. Matching costs
// time proportional to pattern length times name length, and the pattern
// comes from the client.
const maxPatternRunes = 512

// The wildcard characters of a directory search, as they arrive on the
// wire (MS-FSA 2.1.4.4). Windows clients rewrite what the user typed
// before sending it: "install.*" arrives as `install"*`.
const (
	wildStar    = '*' // zero or more characters
	wildOne     = '?' // exactly one character
	wildDOSStar = '<' // zero or more characters, at most up to the last dot
	wildDOSOne  = '>' // one character, or nothing at a dot or at the end
	wildDOSDot  = '"' // a dot, or nothing at the end
)

// matchPattern reports whether name matches the search pattern of a
// directory query. The comparison ignores case. A pattern that is too long
// matches nothing.
func matchPattern(pattern, name string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	if utf8.RuneCountInString(pattern) > maxPatternRunes {
		return false
	}
	n := []rune(name)
	lastDot := -1
	for i, r := range n {
		if r == '.' {
			lastDot = i
		}
	}

	// cur[i] is true if the pattern consumed so far can match name[:i].
	cur := make([]bool, len(n)+1)
	next := make([]bool, len(n)+1)
	cur[0] = true
	for _, p := range pattern {
		clear(next)
		alive := false
		switch p {
		case wildStar:
			seen := false
			for i := range cur {
				seen = seen || cur[i]
				next[i] = seen
			}
			alive = seen
		case wildOne:
			for i := 0; i < len(n); i++ {
				if cur[i] {
					next[i+1] = true
					alive = true
				}
			}
		case wildDOSStar:
			for i := range cur {
				if !cur[i] {
					continue
				}
				// From i it may stop anywhere up to and including the
				// last dot. Without a dot ahead it may run to the end.
				end := len(n)
				if lastDot >= i {
					end = lastDot + 1
				}
				for j := i; j <= end; j++ {
					next[j] = true
				}
				alive = true
			}
		case wildDOSOne:
			for i := range cur {
				if !cur[i] {
					continue
				}
				alive = true
				switch {
				case i == len(n):
					next[i] = true
				case n[i] == '.':
					next[i] = true
					if i+1 == len(n) {
						next[i+1] = true
					}
				default:
					next[i+1] = true
				}
			}
		case wildDOSDot:
			for i := range cur {
				if !cur[i] {
					continue
				}
				if i == len(n) {
					next[i] = true
					alive = true
				} else if n[i] == '.' {
					next[i+1] = true
					alive = true
				}
			}
		default:
			for i := 0; i < len(n); i++ {
				if cur[i] && equalFold(n[i], p) {
					next[i+1] = true
					alive = true
				}
			}
		}
		if !alive {
			return false
		}
		cur, next = next, cur
	}
	return cur[len(n)]
}

// equalFold reports whether two characters are equal when case is ignored.
func equalFold(a, b rune) bool {
	if a == b {
		return true
	}
	for r := unicode.SimpleFold(a); r != a; r = unicode.SimpleFold(r) {
		if r == b {
			return true
		}
	}
	return false
}
