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

package state

import (
	"errors"
	"fmt"
	"strings"
)

// Kind says which identifier of a machine a key holds.
type Kind string

// The kinds of identifier a client can be selected by.
const (
	KindMAC    Kind = "mac"
	KindUUID   Kind = "uuid"
	KindSerial Kind = "serial"
)

// Key selects a machine by one identifier. The identifier is sent by the
// machine itself and is trivial to forge: a key selects, it does not
// authenticate.
type Key struct {
	Kind  Kind   `json:"kind"`
	Value string `json:"value"` // canonical form, see Identity
}

func (k Key) String() string { return string(k.Kind) + ":" + k.Display() }

// Display returns the value the way people write it.
func (k Key) Display() string {
	if k.Kind == KindMAC && len(k.Value) == 12 {
		v := strings.ToLower(k.Value)
		return v[0:2] + ":" + v[2:4] + ":" + v[4:6] + ":" + v[6:8] + ":" + v[8:10] + ":" + v[10:12]
	}
	return k.Value
}

// fileName is the name of the record of this key.
//
// Keys come from the network. The functions that make them leave only
// letters, digits and hyphens in a value, but a Key can also be written as
// a literal, so the name is checked again here, where it becomes part of a
// path: a known kind, and a value that cannot leave the directory.
func (k Key) fileName() (string, error) {
	switch k.Kind {
	case KindMAC, KindUUID, KindSerial:
	default:
		return "", fmt.Errorf("%q is not a kind of client identifier", string(k.Kind))
	}
	if k.Value == "" || len(k.Value) > 64 {
		return "", errors.New("a client identifier has 1 to 64 characters")
	}
	if strings.Contains(k.Value, "/") || strings.Contains(k.Value, "\\") || strings.Contains(k.Value, "..") {
		return "", errors.New("a client identifier cannot contain a path")
	}
	for i := 0; i < len(k.Value); i++ {
		c := k.Value[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return "", errors.New("a client identifier has letters, digits and hyphens only")
		}
	}
	return string(k.Kind) + "-" + k.Value + ".json", nil
}

// Identity is what a booting machine says about itself, in canonical form.
// A field is empty if the machine did not send a usable value.
type Identity struct {
	// MAC is 12 hexadecimal digits in upper case, without separators.
	MAC string `json:"mac,omitempty"`
	// UUID is in the form 8-4-4-4-12 in lower case.
	UUID string `json:"uuid,omitempty"`
	// Serial is the SMBIOS serial number reduced to letters, digits and
	// hyphens, at most 64 of them, without hyphens at either end.
	Serial string `json:"serial,omitempty"`
}

// Keys returns the keys under which this machine can have been armed, in
// the order in which they are looked up.
func (id Identity) Keys() []Key {
	var keys []Key
	if id.MAC != "" {
		keys = append(keys, Key{KindMAC, id.MAC})
	}
	if id.UUID != "" {
		keys = append(keys, Key{KindUUID, id.UUID})
	}
	if id.Serial != "" {
		keys = append(keys, Key{KindSerial, strings.ToUpper(id.Serial)})
	}
	return keys
}

// NewIdentity cleans what a machine sent. Values come from the network and
// are untrusted; whatever does not have the expected form is dropped.
func NewIdentity(mac, uuid, serial string) Identity {
	var id Identity
	id.MAC, _ = canonicalMAC(mac)
	id.UUID, _ = canonicalUUID(uuid)
	if s := sanitize(serial); !placeholderSerial(s) {
		id.Serial = s
	}
	return id
}

// sanitize keeps letters, digits and hyphens, drops hyphens at the ends and
// cuts the result to 64 characters.
func sanitize(s string) string {
	// Whatever the result, it is made from the first kilobyte.
	if len(s) > 1024 {
		s = s[:1024]
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' {
			b.WriteByte(c)
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 64 {
		out = strings.TrimRight(out[:64], "-")
	}
	return out
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// canonicalMAC accepts 12 hexadecimal digits with colons, hyphens, dots or
// nothing between them.
func canonicalMAC(s string) (string, bool) {
	if len(s) > 17 {
		return "", false
	}
	digits := strings.NewReplacer(":", "", "-", "", ".", "").Replace(s)
	if len(digits) != 12 || !isHex(digits) {
		return "", false
	}
	digits = strings.ToUpper(digits)
	// All zeros and the broadcast address identify no machine.
	if digits == "000000000000" || digits == "FFFFFFFFFFFF" {
		return "", false
	}
	return digits, true
}

// canonicalUUID accepts the usual 8-4-4-4-12 form.
func canonicalUUID(s string) (string, bool) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return "", false
	}
	if !isHex(strings.ReplaceAll(s, "-", "")) || strings.Count(s, "-") != 4 {
		return "", false
	}
	s = strings.ToLower(s)
	// Firmware without a real UUID reports all zeros or all ones.
	if strings.Trim(s, "0-") == "" || strings.Trim(s, "f-") == "" {
		return "", false
	}
	return s, true
}

// placeholderSerial reports whether a sanitized serial number is one of the
// texts that firmware reports when the manufacturer never set one. Many
// machines share them, so they identify nothing.
func placeholderSerial(s string) bool {
	if s == "" || strings.Trim(s, "0") == "" {
		return true
	}
	switch strings.ToUpper(s) {
	case "TOBEFILLEDBYOEM", "DEFAULTSTRING", "SYSTEMSERIALNUMBER", "NONE", "NA",
		"NOTSPECIFIED", "NOTAPPLICABLE", "NOTAVAILABLE", "UNKNOWN", "INVALID",
		"SERIALNUMBER", "123456789", "1234567890", "CHASSISSERIALNUMBER", "OEM":
		return true
	}
	return false
}

// ParseKey reads an identifier the way an administrator types it. Without
// a prefix it is taken as a MAC address or a UUID if it has that form, and
// as a serial number otherwise. The prefixes "mac:", "uuid:" and "serial:"
// say which one is meant.
func ParseKey(s string) (Key, error) {
	s = strings.TrimSpace(s)
	kind, value, hasKind := strings.Cut(s, ":")
	switch Kind(strings.ToLower(kind)) {
	case KindMAC:
		if hasKind {
			if v, ok := canonicalMAC(value); ok {
				return Key{KindMAC, v}, nil
			}
			return Key{}, fmt.Errorf("%q is not a MAC address", value)
		}
	case KindUUID:
		if hasKind {
			if v, ok := canonicalUUID(value); ok {
				return Key{KindUUID, v}, nil
			}
			return Key{}, fmt.Errorf("%q is not a UUID of the form xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx", value)
		}
	case KindSerial:
		if hasKind {
			return serialKey(value)
		}
	}
	if v, ok := canonicalMAC(s); ok {
		return Key{KindMAC, v}, nil
	}
	if v, ok := canonicalUUID(s); ok {
		return Key{KindUUID, v}, nil
	}
	return serialKey(s)
}

func serialKey(s string) (Key, error) {
	if s == "" {
		return Key{}, errors.New("no identifier given")
	}
	clean := sanitize(s)
	if placeholderSerial(clean) {
		return Key{}, fmt.Errorf("%q is a placeholder that many machines report; it cannot select one of them. Use an identifier that is unique to the machine", s)
	}
	if clean != s {
		return Key{}, fmt.Errorf("%q is not a MAC address, a UUID or a serial number; serial numbers are compared by their letters, digits and hyphens only, which would be %q", s, clean)
	}
	return Key{KindSerial, strings.ToUpper(clean)}, nil
}
