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
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func newStore(t testing.TB) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func must[T any](v T, err error) func(testing.TB) T {
	return func(t testing.TB) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

var (
	macKey  = Key{KindMAC, "525400770001"}
	machine = NewIdentity("52:54:00:77:00:01", "4C4C4544-0042-3510-8054-B7C04F563432", "PF2ABCDE")
)

func TestParseKey(t *testing.T) {
	good := []struct{ in, want string }{
		{"52:54:00:77:00:01", "mac:52:54:00:77:00:01"},
		{"52-54-00-77-00-01", "mac:52:54:00:77:00:01"},
		{"5254.0077.0001", "mac:52:54:00:77:00:01"},
		{"525400770001", "mac:52:54:00:77:00:01"},
		{"AA:BB:CC:DD:EE:FF", "mac:aa:bb:cc:dd:ee:ff"},
		{"mac:525400770001", "mac:52:54:00:77:00:01"},
		{" 52:54:00:77:00:01 ", "mac:52:54:00:77:00:01"},
		{"4C4C4544-0042-3510-8054-B7C04F563432", "uuid:4c4c4544-0042-3510-8054-b7c04f563432"},
		{"uuid:4c4c4544-0042-3510-8054-b7c04f563432", "uuid:4c4c4544-0042-3510-8054-b7c04f563432"},
		{"PF2ABCDE", "serial:PF2ABCDE"},
		{"pf2abcde", "serial:PF2ABCDE"},
		{"serial:525400770001", "serial:525400770001"}, // looks like a MAC, but the prefix decides
		{"VMware-56-4d", "serial:VMWARE-56-4D"},
	}
	for _, tt := range good {
		k, err := ParseKey(tt.in)
		if err != nil || k.String() != tt.want {
			t.Errorf("ParseKey(%q) = %v, %v; want %s", tt.in, k, err, tt.want)
		}
	}
	bad := []string{
		"", "mac:", "mac:zz:54:00:77:00:01", "mac:52:54:00:77:00", "uuid:1234",
		"00:00:00:00:00:00", "mac:ff:ff:ff:ff:ff:ff",
		"serial:", "To be filled by O.E.M.", "Default string", "0", "000000", "None",
		"serial:Default string", "ABC 123", "abc/def", "../x", "a;b",
		"uuid:00000000-0000-0000-0000-000000000000", "uuid:ffffffff-ffff-ffff-ffff-ffffffffffff",
	}
	for _, in := range bad {
		if k, err := ParseKey(in); err == nil {
			t.Errorf("ParseKey(%q) = %v, want an error", in, k)
		}
	}
}

func TestNewIdentity(t *testing.T) {
	id := NewIdentity("52-54-00-77-00-01", "4C4C4544-0042-3510-8054-B7C04F563432", " PF2/AB CDE\r\n")
	want := Identity{MAC: "525400770001", UUID: "4c4c4544-0042-3510-8054-b7c04f563432", Serial: "PF2ABCDE"}
	if id != want {
		t.Errorf("identity = %+v, want %+v", id, want)
	}
	// Garbage and placeholders are dropped, not passed on.
	id = NewIdentity("not a mac", "${uuid}", "To be filled by O.E.M.")
	if id != (Identity{}) {
		t.Errorf("identity = %+v, want empty", id)
	}
	id = NewIdentity("", "", "-"+strings.Repeat("A", 100)+"-")
	if len(id.Serial) != 64 || strings.Trim(id.Serial, "A") != "" {
		t.Errorf("serial = %q", id.Serial)
	}
	if len(machine.Keys()) != 3 || len((Identity{}).Keys()) != 0 {
		t.Error("Keys returns the wrong number of keys")
	}
}

// TestOneInstallPerArm is the central safety property: arming leads to one
// installation, and nothing but arming again leads to another.
func TestOneInstallPerArm(t *testing.T) {
	s := newStore(t)

	// Not armed: no installer, however often it boots.
	for i := 0; i < 3; i++ {
		if d := must(s.Boot(machine, "10.0.0.5"))(t); d.Install || d.Client.State != Unknown {
			t.Fatalf("boot %d of an unarmed client: %+v", i, d)
		}
	}

	c := must(s.Arm(macKey, "win11", "example", false))(t)
	if c.State != Armed || len(c.Token) != 32 {
		t.Fatalf("after Arm: %+v", c)
	}

	d := must(s.Boot(machine, "10.0.0.5"))(t)
	if !d.Install || d.Client.State != Installing || d.Client.Image != "win11" || d.Client.Profile != "example" || d.Client.Seen != machine {
		t.Fatalf("first boot of an armed client: %+v", d)
	}
	token := d.Client.Token

	// Setup restarts the machine several times. Each time it asks again.
	for i := 0; i < 5; i++ {
		if d := must(s.Boot(machine, "10.0.0.5"))(t); d.Install || d.Client.State != Installing {
			t.Fatalf("boot %d during the installation: %+v", i, d)
		}
	}

	c, changed, err := s.Provisioned(token)
	if err != nil || !changed || c.State != Provisioned || c.FinishedAt == nil {
		t.Fatalf("Provisioned: %+v, %v, %v", c, changed, err)
	}
	for i := 0; i < 3; i++ {
		if d := must(s.Boot(machine, "10.0.0.5"))(t); d.Install || d.Client.State != Provisioned {
			t.Fatalf("boot %d after provisioning: %+v", i, d)
		}
	}

	// Only arming again installs again.
	must(s.Arm(macKey, "win11", "example", false))(t)
	if d := must(s.Boot(machine, "10.0.0.5"))(t); !d.Install {
		t.Fatal("no installation after arming again")
	}
	if d := must(s.Boot(machine, "10.0.0.5"))(t); d.Install {
		t.Fatal("second installation from one Arm")
	}
}

func TestFailedClientIsNotReinstalled(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }

	must(s.Arm(macKey, "win11", "example", false))(t)
	must(s.Boot(machine, ""))(t)

	now = now.Add(2 * time.Hour)
	if failed := must(s.Expire(3 * time.Hour))(t); len(failed) != 0 {
		t.Fatalf("expired too early: %+v", failed)
	}
	now = now.Add(2 * time.Hour)
	failed := must(s.Expire(3 * time.Hour))(t)
	if len(failed) != 1 || failed[0].State != Failed || failed[0].Failure == "" {
		t.Fatalf("Expire = %+v", failed)
	}
	if d := must(s.Boot(machine, ""))(t); d.Install || d.Client.State != Failed {
		t.Fatalf("boot of a failed client: %+v", d)
	}
	// Its token is dead: no files, no late callback.
	if _, err := s.Installation(failed[0].Token); !errors.Is(err, ErrNoToken) {
		t.Errorf("Installation after failure: %v", err)
	}
	if _, _, err := s.Provisioned(failed[0].Token); !errors.Is(err, ErrNoToken) {
		t.Errorf("Provisioned after failure: %v", err)
	}
}

func TestFail(t *testing.T) {
	s := newStore(t)
	armed := must(s.Arm(macKey, "win11", "example", false))(t)
	if err := s.Fail(armed.Token, "too early"); !errors.Is(err, ErrNoToken) {
		t.Errorf("Fail before the installation started: %v", err)
	}
	must(s.Boot(machine, ""))(t)
	if err := s.Fail(armed.Token, "the image is gone"); err != nil {
		t.Fatal(err)
	}
	c, _, _ := s.Get(macKey)
	if c.State != Failed || c.Failure != "the image is gone" || c.FinishedAt == nil {
		t.Errorf("after Fail: %+v", c)
	}
	if d := must(s.Boot(machine, ""))(t); d.Install {
		t.Error("a failed client was installed again")
	}
	if err := s.Fail(armed.Token, "again"); !errors.Is(err, ErrNoToken) {
		t.Errorf("second Fail: %v", err)
	}
}

func TestTokens(t *testing.T) {
	s := newStore(t)
	armed := must(s.Arm(macKey, "win11", "example", false))(t)

	// Armed but not booted: the token opens nothing yet.
	if _, err := s.Installation(armed.Token); !errors.Is(err, ErrNoToken) {
		t.Errorf("Installation before boot: %v", err)
	}
	if _, _, err := s.Provisioned(armed.Token); !errors.Is(err, ErrNoToken) {
		t.Errorf("Provisioned before boot: %v", err)
	}

	must(s.Boot(machine, ""))(t)
	if c, err := s.Installation(armed.Token); err != nil || c.Key != macKey {
		t.Errorf("Installation: %+v, %v", c, err)
	}
	for _, bad := range []string{"", "x", strings.Repeat("0", 32), armed.Token[:31], armed.Token + "0", strings.ToUpper(armed.Token) + "x"} {
		if _, err := s.Installation(bad); !errors.Is(err, ErrNoToken) {
			t.Errorf("Installation(%q): %v", bad, err)
		}
		if _, _, err := s.Provisioned(bad); !errors.Is(err, ErrNoToken) {
			t.Errorf("Provisioned(%q): %v", bad, err)
		}
	}

	if _, changed, err := s.Provisioned(armed.Token); err != nil || !changed {
		t.Fatalf("Provisioned: %v, %v", changed, err)
	}
	// Reporting twice is harmless and changes nothing.
	if c, changed, err := s.Provisioned(armed.Token); err != nil || changed || c.State != Provisioned {
		t.Errorf("second Provisioned: %+v, %v, %v", c, changed, err)
	}
	// The installation is over, so its files are gone.
	if _, err := s.Installation(armed.Token); !errors.Is(err, ErrNoToken) {
		t.Errorf("Installation after provisioning: %v", err)
	}

	// Arming again makes a new token; the old one stays dead.
	again := must(s.Arm(macKey, "win11", "example", false))(t)
	if again.Token == armed.Token {
		t.Error("token reused")
	}
	must(s.Boot(machine, ""))(t)
	if _, _, err := s.Provisioned(armed.Token); !errors.Is(err, ErrNoToken) {
		t.Errorf("old token still works: %v", err)
	}
}

func TestArmWhileInstalling(t *testing.T) {
	s := newStore(t)
	must(s.Arm(macKey, "win11", "a", false))(t)
	// Arming an armed client replaces what it is armed for.
	must(s.Arm(macKey, "win11", "b", false))(t)
	if d := must(s.Boot(machine, ""))(t); d.Client.Profile != "b" {
		t.Fatalf("profile = %q", d.Client.Profile)
	}
	if _, err := s.Arm(macKey, "win11", "c", false); !errors.Is(err, ErrInstalling) {
		t.Fatalf("Arm while installing: %v", err)
	}
	if c := must(s.Arm(macKey, "win11", "c", true))(t); c.State != Armed {
		t.Fatalf("forced Arm: %+v", c)
	}
}

func TestDisarm(t *testing.T) {
	s := newStore(t)
	if _, err := s.Disarm(macKey); !errors.Is(err, ErrNotArmed) {
		t.Errorf("Disarm of an unknown client: %v", err)
	}

	// Armed and never seen: disarming forgets it.
	must(s.Arm(macKey, "win11", "example", false))(t)
	must(s.Disarm(macKey))(t)
	if _, ok, _ := s.Get(macKey); ok {
		t.Error("record still there after Disarm")
	}
	if d := must(s.Boot(machine, ""))(t); d.Install {
		t.Fatal("a disarmed client was installed")
	}

	// Seen before: disarming puts it back to unknown.
	must(s.Arm(macKey, "win11", "example", false))(t)
	c := must(s.Disarm(macKey))(t)
	if c.State != Unknown || c.Token != "" {
		t.Errorf("after Disarm: %+v", c)
	}
	if d := must(s.Boot(machine, ""))(t); d.Install {
		t.Fatal("a disarmed client was installed")
	}

	// Disarming does not undo an installation that has started.
	must(s.Arm(macKey, "win11", "example", false))(t)
	must(s.Boot(machine, ""))(t)
	if _, err := s.Disarm(macKey); !errors.Is(err, ErrNotArmed) {
		t.Errorf("Disarm while installing: %v", err)
	}
}

func TestArmedBySerialOrUUID(t *testing.T) {
	for _, key := range []string{"PF2ABCDE", "4c4c4544-0042-3510-8054-b7c04f563432"} {
		s := newStore(t)
		// The machine was seen before and is listed under its MAC address.
		must(s.Boot(machine, ""))(t)
		k := must(ParseKey(key))(t)
		must(s.Arm(k, "win11", "example", false))(t)

		// Another machine does not get the installer.
		other := NewIdentity("52:54:00:77:00:02", "", "OTHER")
		if d := must(s.Boot(other, ""))(t); d.Install {
			t.Fatalf("%s: the wrong machine was installed", key)
		}
		d := must(s.Boot(machine, ""))(t)
		if !d.Install || d.Client.Key != k {
			t.Fatalf("%s: %+v", key, d)
		}
		// One record for the machine now, not two.
		if _, ok, _ := s.Get(macKey); ok {
			t.Errorf("%s: the unknown record is still there", key)
		}
		if d := must(s.Boot(machine, ""))(t); d.Install {
			t.Fatalf("%s: installed twice", key)
		}
	}
}

func TestMachineWithoutIdentifiers(t *testing.T) {
	s := newStore(t)
	d := must(s.Boot(Identity{}, ""))(t)
	if d.Install || d.Client.Key != (Key{}) {
		t.Errorf("decision = %+v", d)
	}
	if list := must(s.List())(t); len(list) != 0 {
		t.Errorf("a record was written: %+v", list)
	}
}

func TestUnknownClientsAreLimited(t *testing.T) {
	s := newStore(t)
	// Write the records directly; a thousand Boot calls with a sync each
	// would take too long.
	for i := 0; i < maxUnknown; i++ {
		k := Key{KindMAC, "0200000" + strings.ToUpper(strings.Repeat("0", 5-len(itoa16(i)))+itoa16(i))}
		if err := s.write(&Client{Key: k, State: Unknown}); err != nil {
			t.Fatal(err)
		}
	}
	d := must(s.Boot(machine, ""))(t)
	if d.Install || d.Client.Key != (Key{}) {
		t.Errorf("decision = %+v", d)
	}
	if _, ok, _ := s.Get(macKey); ok {
		t.Error("record written although the limit was reached")
	}
	// The limit does not get in the way of arming.
	must(s.Arm(macKey, "win11", "example", false))(t)
	if d := must(s.Boot(machine, ""))(t); !d.Install {
		t.Error("an armed client was not installed because of the limit")
	}
}

func itoa16(i int) string {
	const digits = "0123456789abcdef"
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 16 {
		b = append([]byte{digits[i%16]}, b...)
	}
	return string(b)
}

// TestConcurrentBoots lets many processes' worth of stores answer the same
// armed client at once. Exactly one may hand out the installer.
func TestConcurrentBoots(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	first := must(Open(dir))(t)
	must(first.Arm(macKey, "win11", "example", false))(t)

	const n = 24
	var wg sync.WaitGroup
	installs := make(chan bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Open(dir)
			if err != nil {
				t.Error(err)
				return
			}
			d, err := s.Boot(machine, "")
			if err != nil {
				t.Error(err)
			}
			installs <- d.Install
		}()
	}
	wg.Wait()
	close(installs)
	count := 0
	for in := range installs {
		if in {
			count++
		}
	}
	if count != 1 {
		t.Errorf("%d installations from one Arm", count)
	}
}

func TestRecordsOnDisk(t *testing.T) {
	s := newStore(t)
	must(s.Arm(macKey, "win11", "example", false))(t)
	must(s.Boot(machine, "10.0.0.5"))(t)

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if got := strings.Join(names, " "); got != ".lock mac-525400770001.json" {
		t.Errorf("files = %q", got)
	}
	info, _ := os.Stat(filepath.Join(s.dir, "mac-525400770001.json"))
	if info.Mode().Perm()&0o007 != 0 {
		t.Errorf("record is readable by everyone: %v", info.Mode())
	}

	// A record that names another client than its file is refused, so that
	// nobody arms a machine by copying a file.
	data, _ := os.ReadFile(filepath.Join(s.dir, "mac-525400770001.json"))
	os.WriteFile(filepath.Join(s.dir, "mac-525400770002.json"), data, 0o640)
	if _, err := s.List(); err == nil {
		t.Error("a record under the wrong file name was accepted")
	}
	os.Remove(filepath.Join(s.dir, "mac-525400770002.json"))

	// Damaged records are an error, not an empty list.
	os.WriteFile(filepath.Join(s.dir, "mac-525400770001.json"), []byte("{"), 0o640)
	if _, err := s.List(); err == nil {
		t.Error("a damaged record was ignored")
	}
	if _, err := s.Boot(machine, ""); err == nil {
		t.Error("Boot ignored a damaged record")
	}
}

var safeValue = regexp.MustCompile(`^[A-Za-z0-9-]*$`)

func FuzzIdentity(f *testing.F) {
	f.Add("52:54:00:77:00:01", "4C4C4544-0042-3510-8054-B7C04F563432", "PF2ABCDE")
	f.Add("../../etc/passwd", "${uuid}", "a&b|c>d\r\n")
	f.Add("525400770001", "", "To be filled by O.E.M.")
	f.Fuzz(func(t *testing.T, mac, uuid, serial string) {
		id := NewIdentity(mac, uuid, serial)
		for _, v := range []string{id.MAC, id.UUID, id.Serial} {
			if !safeValue.MatchString(v) || len(v) > 64 || strings.HasPrefix(v, "-") || strings.HasSuffix(v, "-") {
				t.Errorf("unsafe value %q from (%q, %q, %q)", v, mac, uuid, serial)
			}
		}
		for _, k := range id.Keys() {
			name := k.fileName()
			if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") || filepath.Base(name) != name {
				t.Errorf("unsafe file name %q", name)
			}
			// What Kyro shows can be typed back in.
			back, err := ParseKey(k.String())
			if err != nil || back != k {
				t.Errorf("ParseKey(%q) = %v, %v; want %v", k.String(), back, err, k)
			}
		}
		for _, in := range []string{mac, uuid, serial} {
			if k, err := ParseKey(in); err == nil && !safeValue.MatchString(k.Value) {
				t.Errorf("ParseKey(%q) accepted the unsafe value %q", in, k.Value)
			}
		}
	})
}

// FuzzTransitions runs random sequences of operations on two machines and
// checks the rules that must hold whatever the sequence.
func FuzzTransitions(f *testing.F) {
	f.Add([]byte{0, 2, 2, 4, 2})
	f.Add([]byte{0, 2, 6, 2, 0, 2, 2})
	f.Add([]byte{1, 3, 5, 3, 7, 3, 9, 3})
	f.Add([]byte{0, 8, 2, 0, 10, 2, 4, 2})
	f.Fuzz(func(t *testing.T, ops []byte) {
		if len(ops) > 64 {
			return
		}
		s := newStore(t)
		now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
		s.Now = func() time.Time { return now }
		ids := []Identity{machine, NewIdentity("52:54:00:77:00:02", "", "OTHER-2")}
		keys := []Key{macKey, {KindSerial, "OTHER-2"}}
		var arms, installs [2]int
		var tokens [2]string

		for _, op := range ops {
			m := int(op) % 2
			switch op / 2 % 6 {
			case 0:
				if c, err := s.Arm(keys[m], "img", "prof", false); err == nil {
					arms[m]++
					tokens[m] = c.Token
				}
			case 1:
				d, err := s.Boot(ids[m], "")
				if err != nil {
					t.Fatal(err)
				}
				if d.Install {
					installs[m]++
				}
			case 2:
				s.Provisioned(tokens[m])
			case 3:
				s.Disarm(keys[m])
			case 4:
				now = now.Add(2 * time.Hour)
				s.Expire(3 * time.Hour)
			case 5:
				if c, err := s.Arm(keys[m], "img", "prof", true); err == nil {
					arms[m]++
					tokens[m] = c.Token
				}
			}
			for i := range ids {
				if installs[i] > arms[i] {
					t.Fatalf("machine %d: %d installations from %d arms (ops %v)", i, installs[i], arms[i], ops)
				}
			}
		}

		// Whatever happened, without a new Arm nobody gets the installer
		// from two boots in a row.
		for i, id := range ids {
			first, _ := s.Boot(id, "")
			second, _ := s.Boot(id, "")
			if second.Install {
				t.Fatalf("machine %d was installed on two boots in a row (first: %v)", i, first.Install)
			}
		}
		list, err := s.List()
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range list {
			switch c.State {
			case Unknown, Armed, Installing, Provisioned, Failed:
			default:
				t.Fatalf("record in state %q", c.State)
			}
			if (c.State == Armed || c.State == Installing) && len(c.Token) != 32 {
				t.Fatalf("%s is %s without a token", c.Key, c.State)
			}
		}
	})
}
