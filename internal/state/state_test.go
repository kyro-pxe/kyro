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

// arming is an arming with the default lifetime.
func arming(image, profile string) Arming {
	return Arming{Image: image, Profile: profile, Lifetime: DefaultArmLifetime}
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

	c := must(s.Arm(macKey, arming("win11", "example"), false))(t)
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
	must(s.Arm(macKey, arming("win11", "example"), false))(t)
	if d := must(s.Boot(machine, "10.0.0.5"))(t); !d.Install {
		t.Fatal("no installation after arming again")
	}
	if d := must(s.Boot(machine, "10.0.0.5"))(t); d.Install {
		t.Fatal("second installation from one Arm")
	}
}

// TestArmingRunsOut: a client that was armed and forgotten is not wiped
// when it boots from the network after its arming has run out.
func TestArmingRunsOut(t *testing.T) {
	start := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	twoHours := Arming{Image: "win11", Profile: "example", Lifetime: 2 * time.Hour}
	fixture := func(t *testing.T) (*Store, *time.Time) {
		s, now := newStore(t), start
		s.Now = func() time.Time { return now }
		return s, &now
	}

	t.Run("a boot just before the end installs", func(t *testing.T) {
		s, now := fixture(t)
		c := must(s.Arm(macKey, twoHours, false))(t)
		if c.ArmedUntil == nil || !c.ArmedUntil.Equal(start.Add(2*time.Hour)) {
			t.Fatalf("ArmedUntil = %v", c.ArmedUntil)
		}
		*now = start.Add(2*time.Hour - time.Second)
		if d := must(s.Boot(machine, ""))(t); !d.Install {
			t.Fatalf("boot before the end: %+v", d)
		}
	})

	t.Run("a boot at the end does not", func(t *testing.T) {
		s, now := fixture(t)
		armed := must(s.Arm(macKey, twoHours, false))(t)
		*now = start.Add(2 * time.Hour)
		d := must(s.Boot(machine, "10.0.0.5"))(t)
		if d.Install || d.Client.State != Expired || d.Client.Token != "" || d.Client.Failure == "" {
			t.Fatalf("boot at the end: %+v", d)
		}
		if d.Client.Seen != machine || d.Client.Boots != 1 {
			t.Errorf("the boot was not recorded: %+v", d.Client)
		}
		// Nothing brings it back but arming again.
		*now = start.Add(100 * time.Hour)
		for i := 0; i < 3; i++ {
			if d := must(s.Boot(machine, ""))(t); d.Install || d.Client.State != Expired {
				t.Fatalf("later boot %d: %+v", i, d)
			}
		}
		if _, err := s.Installation(armed.Token); !errors.Is(err, ErrNoToken) {
			t.Errorf("the token of the arming still opens something: %v", err)
		}
		if _, _, err := s.Provisioned(armed.Token); !errors.Is(err, ErrNoToken) {
			t.Errorf("the token of the arming still reports: %v", err)
		}
		if c, err := s.Disarm(macKey); !errors.Is(err, ErrNotArmed) || c.State != Expired {
			t.Errorf("Disarm of an expired client: %+v, %v", c, err)
		}
		if changed := must(s.Expire(time.Hour))(t); len(changed) != 0 {
			t.Errorf("Expire changed it again: %+v", changed)
		}
	})

	t.Run("without a boot the sweep marks it", func(t *testing.T) {
		s, now := fixture(t)
		must(s.Arm(macKey, twoHours, false))(t)
		*now = start.Add(time.Hour)
		if changed := must(s.Expire(3 * time.Hour))(t); len(changed) != 0 {
			t.Fatalf("expired too early: %+v", changed)
		}
		*now = start.Add(2 * time.Hour)
		changed := must(s.Expire(3 * time.Hour))(t)
		if len(changed) != 1 || changed[0].State != Expired || changed[0].Token != "" {
			t.Fatalf("Expire = %+v", changed)
		}
		if d := must(s.Boot(machine, ""))(t); d.Install {
			t.Fatal("installed after the sweep marked it expired")
		}
	})

	t.Run("disarming after the end marks it expired", func(t *testing.T) {
		s, now := fixture(t)
		must(s.Arm(macKey, twoHours, false))(t)
		*now = start.Add(3 * time.Hour)
		if c, err := s.Disarm(macKey); !errors.Is(err, ErrNotArmed) || c.State != Expired {
			t.Fatalf("Disarm = %+v, %v", c, err)
		}
		if c, ok, _ := s.Get(macKey); !ok || c.State != Expired {
			t.Fatalf("record after Disarm: %+v", c)
		}
	})

	t.Run("arming again installs", func(t *testing.T) {
		s, now := fixture(t)
		must(s.Arm(macKey, twoHours, false))(t)
		*now = start.Add(5 * time.Hour)
		must(s.Boot(machine, ""))(t)
		c := must(s.Arm(macKey, twoHours, false))(t)
		if c.State != Armed || c.Failure != "" || c.FinishedAt != nil || !c.ArmedUntil.Equal(now.Add(2*time.Hour)) {
			t.Fatalf("armed again: %+v", c)
		}
		if d := must(s.Boot(machine, ""))(t); !d.Install {
			t.Fatalf("boot after arming again: %+v", d)
		}
	})

	t.Run("an expired arming does not hide a valid one", func(t *testing.T) {
		s, now := fixture(t)
		must(s.Arm(macKey, twoHours, false))(t)
		*now = start.Add(3 * time.Hour)
		bySerial := Key{KindSerial, machine.Serial}
		must(s.Arm(bySerial, twoHours, false))(t)
		d := must(s.Boot(machine, ""))(t)
		if !d.Install || d.Client.Key != bySerial {
			t.Fatalf("boot: %+v", d)
		}
		if c, _, _ := s.Get(macKey); c.State != Expired {
			t.Errorf("the record under the MAC address: %+v", c)
		}
	})

	t.Run("a record from before this rule gets the default lifetime", func(t *testing.T) {
		s, now := fixture(t)
		legacy := func(k Key, armedAt *time.Time) {
			t.Helper()
			if err := s.write(&Client{Key: k, State: Armed, Image: "win11", Profile: "example", Token: strings.Repeat("a", 32), ArmedAt: armedAt}); err != nil {
				t.Fatal(err)
			}
		}
		legacy(macKey, &start)
		*now = start.Add(DefaultArmLifetime - time.Second)
		if d := must(s.Boot(machine, ""))(t); !d.Install {
			t.Fatalf("within the default lifetime: %+v", d)
		}

		s, now = fixture(t)
		legacy(macKey, &start)
		*now = start.Add(DefaultArmLifetime)
		if d := must(s.Boot(machine, ""))(t); d.Install || d.Client.State != Expired {
			t.Fatalf("after the default lifetime: %+v", d)
		}

		// No time at all: when in doubt, no installation.
		s, _ = fixture(t)
		legacy(macKey, nil)
		if d := must(s.Boot(machine, ""))(t); d.Install || d.Client.State != Expired {
			t.Fatalf("armed without a time: %+v", d)
		}
	})

	t.Run("the lifetime has limits", func(t *testing.T) {
		s, _ := fixture(t)
		for _, d := range []time.Duration{-time.Hour, 0, MinArmLifetime - time.Second, MaxArmLifetime + time.Second} {
			if _, err := s.Arm(macKey, Arming{Image: "win11", Profile: "example", Lifetime: d}, false); !errors.Is(err, ErrLifetime) {
				t.Errorf("lifetime %v: %v", d, err)
			}
		}
		if _, ok, _ := s.Get(macKey); ok {
			t.Error("a refused arming left a record")
		}
		for _, d := range []time.Duration{MinArmLifetime, MaxArmLifetime} {
			if _, err := s.Arm(macKey, Arming{Image: "win11", Profile: "example", Lifetime: d}, false); err != nil {
				t.Errorf("lifetime %v: %v", d, err)
			}
		}
	})
}

// TestStages: the record follows an installation through what Kyro sees
// and what Windows PE reports, forwards only.
func TestStages(t *testing.T) {
	start := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	fixture := func(t *testing.T) (*Store, *time.Time, string) {
		s, now := newStore(t), start
		s.Now = func() time.Time { return now }
		must(s.Arm(macKey, arming("win11", "example"), false))(t)
		d := must(s.Boot(machine, ""))(t)
		if d.Client.Stage != StageInstallerSent || !d.Client.StageAt.Equal(start) || d.Client.Restarts != 0 {
			t.Fatalf("after the installer was handed out: %+v", d.Client)
		}
		return s, &now, d.Client.Token
	}
	report := func(t *testing.T, s *Store, token, event string, wantChanged bool, want Stage) Client {
		t.Helper()
		c, changed, err := s.Report(token, event, "")
		if err != nil || changed != wantChanged || c.Stage != want {
			t.Fatalf("Report(%s) = stage %s, changed %v, %v; want stage %s, changed %v", event, c.Stage, changed, err, want, wantChanged)
		}
		return c
	}

	t.Run("an installation that goes well", func(t *testing.T) {
		s, now, token := fixture(t)
		*now = start.Add(2 * time.Minute)
		c := report(t, s, token, EventShareConnected, true, StageShareConnected)
		if !c.StageAt.Equal(*now) {
			t.Errorf("StageAt = %v", c.StageAt)
		}
		report(t, s, token, EventShareConnected, false, StageShareConnected)
		report(t, s, token, EventSetupStarted, true, StageSetupStarted)
		// A report that comes late does not move the record back.
		report(t, s, token, EventShareConnected, false, StageSetupStarted)

		*now = start.Add(10 * time.Minute)
		if d := must(s.Boot(machine, ""))(t); d.Install || d.Client.Stage != StageRestarted || d.Client.Restarts != 1 || !d.Client.StageAt.Equal(*now) {
			t.Fatalf("first restart: %+v", d.Client)
		}
		*now = start.Add(15 * time.Minute)
		if d := must(s.Boot(machine, ""))(t); d.Client.Restarts != 2 || !d.Client.StageAt.Equal(start.Add(10*time.Minute)) {
			t.Fatalf("second restart: %+v", d.Client)
		}
		report(t, s, token, EventSetupFinished, false, StageRestarted)

		c, _, err := s.Provisioned(token)
		if err != nil || c.State != Provisioned || c.Stage != StageRestarted || c.Restarts != 2 {
			t.Fatalf("provisioned: %+v, %v", c, err)
		}
		// The installation is over: no more reports, no more counting.
		if _, _, err := s.Report(token, EventSetupStarted, ""); !errors.Is(err, ErrNoToken) {
			t.Errorf("a report after the installation: %v", err)
		}
		if d := must(s.Boot(machine, ""))(t); d.Client.Restarts != 2 {
			t.Errorf("a boot after the installation was counted as a restart: %+v", d.Client)
		}
		// Arming again starts from nothing.
		if c := must(s.Arm(macKey, arming("win11", "example"), false))(t); c.Stage != "" || c.StageAt != nil || c.Restarts != 0 {
			t.Errorf("armed again: %+v", c)
		}
	})

	t.Run("Setup fails in Windows PE", func(t *testing.T) {
		s, _, token := fixture(t)
		report(t, s, token, EventSetupStarted, true, StageSetupStarted)
		c, changed, err := s.Report(token, EventSetupFailed, "-2147023436")
		if err != nil || !changed || c.State != Failed || !strings.Contains(c.Failure, "exit code -2147023436") || !strings.Contains(c.Failure, `X:\Windows\Panther`) {
			t.Fatalf("setup-failed: %+v, %v", c, err)
		}
		if d := must(s.Boot(machine, ""))(t); d.Install || d.Client.State != Failed {
			t.Fatalf("boot after the failure: %+v", d)
		}
		if _, err := s.Installation(token); !errors.Is(err, ErrNoToken) {
			t.Errorf("the token still opens the files: %v", err)
		}
		if _, _, err := s.Report(token, EventSetupFailed, "1"); !errors.Is(err, ErrNoToken) {
			t.Errorf("a second failure report: %v", err)
		}
	})

	t.Run("what is not a report", func(t *testing.T) {
		s, _, token := fixture(t)
		if _, _, err := s.Report(token, "provisioned", ""); err == nil || errors.Is(err, ErrNoToken) {
			t.Errorf("an unknown event: %v", err)
		}
		for _, bad := range []string{"", "short", strings.Repeat("0", 32), token[:31] + "x"} {
			if bad == token {
				continue
			}
			if _, _, err := s.Report(bad, EventShareConnected, ""); !errors.Is(err, ErrNoToken) {
				t.Errorf("token %q: %v", bad, err)
			}
		}
		// An armed client that has not booted has a token, and no
		// installation to report on.
		other := Key{KindSerial, "OTHER"}
		armed := must(s.Arm(other, arming("win11", "example"), false))(t)
		if _, _, err := s.Report(armed.Token, EventShareConnected, ""); !errors.Is(err, ErrNoToken) {
			t.Errorf("a report for a client that is only armed: %v", err)
		}
		if c, _, _ := s.Get(macKey); c.Stage != StageInstallerSent {
			t.Errorf("the record changed: %+v", c)
		}
	})

	// When the time is up, the failure says where the installation stopped.
	for _, tt := range []struct {
		name   string
		events []string
		boots  int
		want   string
	}{
		{"nothing heard from Windows PE", nil, 0, "never connected to the install share"},
		{"Setup started and nothing more", []string{EventShareConnected, EventSetupStarted}, 0, "found no disk"},
		{"restarted and no callback", []string{EventShareConnected, EventSetupStarted}, 3, "restarted 3 times, but the installed system did not call back"},
	} {
		t.Run("timeout: "+tt.name, func(t *testing.T) {
			s, now, token := fixture(t)
			for _, e := range tt.events {
				must2(s.Report(token, e, ""))(t)
			}
			for i := 0; i < tt.boots; i++ {
				must(s.Boot(machine, ""))(t)
			}
			*now = start.Add(4 * time.Hour)
			failed := must(s.Expire(3 * time.Hour))(t)
			if len(failed) != 1 || !strings.Contains(failed[0].Failure, "did not report back within 3h0m0s") || !strings.Contains(failed[0].Failure, tt.want) {
				t.Fatalf("Expire = %+v, want a failure with %q", failed, tt.want)
			}
		})
	}
}

// must2 is must for functions with two results before the error.
func must2[A, B any](a A, b B, err error) func(testing.TB) A {
	return func(t testing.TB) A {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
}

func TestFailedClientIsNotReinstalled(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }

	must(s.Arm(macKey, arming("win11", "example"), false))(t)
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
	armed := must(s.Arm(macKey, arming("win11", "example"), false))(t)
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
	armed := must(s.Arm(macKey, arming("win11", "example"), false))(t)

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
	again := must(s.Arm(macKey, arming("win11", "example"), false))(t)
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
	must(s.Arm(macKey, arming("win11", "a"), false))(t)
	// Arming an armed client replaces what it is armed for.
	must(s.Arm(macKey, arming("win11", "b"), false))(t)
	if d := must(s.Boot(machine, ""))(t); d.Client.Profile != "b" {
		t.Fatalf("profile = %q", d.Client.Profile)
	}
	if _, err := s.Arm(macKey, arming("win11", "c"), false); !errors.Is(err, ErrInstalling) {
		t.Fatalf("Arm while installing: %v", err)
	}
	if c := must(s.Arm(macKey, arming("win11", "c"), true))(t); c.State != Armed {
		t.Fatalf("forced Arm: %+v", c)
	}
}

func TestDisarm(t *testing.T) {
	s := newStore(t)
	if _, err := s.Disarm(macKey); !errors.Is(err, ErrNotArmed) {
		t.Errorf("Disarm of an unknown client: %v", err)
	}

	// Armed and never seen: disarming forgets it.
	must(s.Arm(macKey, arming("win11", "example"), false))(t)
	must(s.Disarm(macKey))(t)
	if _, ok, _ := s.Get(macKey); ok {
		t.Error("record still there after Disarm")
	}
	if d := must(s.Boot(machine, ""))(t); d.Install {
		t.Fatal("a disarmed client was installed")
	}

	// Seen before: disarming puts it back to unknown.
	must(s.Arm(macKey, arming("win11", "example"), false))(t)
	c := must(s.Disarm(macKey))(t)
	if c.State != Unknown || c.Token != "" {
		t.Errorf("after Disarm: %+v", c)
	}
	if d := must(s.Boot(machine, ""))(t); d.Install {
		t.Fatal("a disarmed client was installed")
	}

	// Disarming does not undo an installation that has started.
	must(s.Arm(macKey, arming("win11", "example"), false))(t)
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
		must(s.Arm(k, arming("win11", "example"), false))(t)

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
	must(s.Arm(macKey, arming("win11", "example"), false))(t)
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
	must(first.Arm(macKey, arming("win11", "example"), false))(t)

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
	must(s.Arm(macKey, arming("win11", "example"), false))(t)
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
			name, err := k.fileName()
			if err != nil {
				t.Errorf("%v has no file name: %v", k, err)
			}
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
	f.Add([]byte{0, 8, 8, 8, 2, 0, 8, 2, 1, 9, 9, 9, 3})
	f.Add([]byte{0, 2, 12, 26, 2, 40, 2, 4, 12, 54})
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
		// Short enough that the clock of this test gets past it.
		short := Arming{Image: "img", Profile: "prof", Lifetime: 5 * time.Hour}
		var until [2]time.Time

		for _, op := range ops {
			m := int(op) % 2
			switch op / 2 % 7 {
			case 0:
				if c, err := s.Arm(keys[m], short, false); err == nil {
					arms[m]++
					tokens[m], until[m] = c.Token, *c.ArmedUntil
				}
			case 1:
				d, err := s.Boot(ids[m], "")
				if err != nil {
					t.Fatal(err)
				}
				if d.Install {
					installs[m]++
					if !now.Before(until[m]) {
						t.Fatalf("machine %d was installed at %v, its arming ran out at %v (ops %v)", m, now, until[m], ops)
					}
				}
			case 2:
				s.Provisioned(tokens[m])
			case 3:
				s.Disarm(keys[m])
			case 4:
				now = now.Add(2 * time.Hour)
				s.Expire(3 * time.Hour)
			case 5:
				if c, err := s.Arm(keys[m], short, true); err == nil {
					arms[m]++
					tokens[m], until[m] = c.Token, *c.ArmedUntil
				}
			case 6:
				events := []string{EventShareConnected, EventSetupStarted, EventSetupFinished, EventSetupFailed}
				before, _, _ := s.Get(keys[m])
				c, changed, err := s.Report(tokens[m], events[int(op/14)%len(events)], "1")
				if err == nil && changed && before.State != Installing {
					t.Fatalf("a report changed a client in state %s to %+v (ops %v)", before.State, c, ops)
				}
				if err == nil && c.State != Failed && c.Stage.rank() < before.Stage.rank() {
					t.Fatalf("a report moved the stage back from %s to %s (ops %v)", before.Stage, c.Stage, ops)
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
			case Unknown, Armed, Installing, Provisioned, Failed, Expired:
			default:
				t.Fatalf("record in state %q", c.State)
			}
			if (c.State == Armed || c.State == Installing) && len(c.Token) != 32 {
				t.Fatalf("%s is %s without a token", c.Key, c.State)
			}
			if c.State == Expired && c.Token != "" {
				t.Fatalf("%s is expired and has a token", c.Key)
			}
		}
	})
}

// A key that was not made by ParseKey or NewIdentity must not reach the
// file system.
func TestKeysThatWouldLeaveTheDirectory(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "outside.json")
	if err := os.WriteFile(outside, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, k := range []Key{
		{KindSerial, "../outside"},
		{KindSerial, "x/../../outside"},
		{KindSerial, `..\outside`},
		{KindSerial, ""},
		{KindSerial, "a b"},
		{KindSerial, strings.Repeat("A", 65)},
		{Kind("../x"), "ABC"},
		{Kind(""), "ABC"},
	} {
		if name, err := k.fileName(); err == nil {
			t.Errorf("%#v got the file name %q", k, name)
		}
		if _, err := s.Arm(k, arming("image", "profile"), false); err == nil {
			t.Errorf("%#v was armed", k)
		}
		if err := s.Forget(k); err == nil {
			t.Errorf("%#v was forgotten", k)
		}
		if _, _, err := s.Get(k); err == nil {
			t.Errorf("%#v was read", k)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("the file outside the store is gone: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "state"))
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			t.Errorf("a record was written: %s", e.Name())
		}
	}
}
