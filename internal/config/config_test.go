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

package config

import (
	"strings"
	"testing"
	"time"
)

const minimal = `
interface: eth1
address: 192.168.77.1
smb:
  password: not-a-secret
`

func TestParseAppliesDefaults(t *testing.T) {
	cfg, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "/var/lib/kyro" || cfg.DHCP.Mode != "proxy" || !cfg.TFTPEnabled() ||
		cfg.HTTP.Port != 80 || cfg.SMB.Mode != "embedded" || cfg.SMB.Port != 445 || cfg.SMB.Username != "kyro" ||
		cfg.Status.Listen != "127.0.0.1:8080" || cfg.UnknownClients != "local" ||
		time.Duration(cfg.InstallTimeout) != 3*time.Hour {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if cfg.HTTPBase() != "http://192.168.77.1" {
		t.Errorf("HTTPBase = %q", cfg.HTTPBase())
	}
	if cfg.StateDir() != "/var/lib/kyro/state" || cfg.ImagesDir() != "/var/lib/kyro/images" {
		t.Errorf("directories: %q %q", cfg.StateDir(), cfg.ImagesDir())
	}
}

func TestParseFull(t *testing.T) {
	cfg, err := Parse([]byte(`
interface: br-boot
address: 10.0.0.2
data_dir: /srv/kyro
dhcp:
  mode: full
  range_start: 10.0.0.100
  range_end: 10.0.0.199
  netmask: 255.255.255.0
  router: 10.0.0.1
  dns: [10.0.0.1, 9.9.9.9]
  lease_time: 30m
tftp:
  enabled: false
http:
  port: 8080
smb:
  mode: embedded
  port: 4445
  username: installer
  password: pw-1
status:
  listen: ""
unknown_clients: menu
install_timeout: 90m
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TFTPEnabled() || cfg.HTTPBase() != "http://10.0.0.2:8080" || cfg.Status.Listen != "" ||
		time.Duration(cfg.DHCP.LeaseTime) != 30*time.Minute || len(cfg.DHCP.DNS) != 2 {
		t.Errorf("%+v", cfg)
	}
}

func TestParseRejects(t *testing.T) {
	base := func(extra string) string { return minimal + extra }
	tests := []struct {
		name, yaml, want string
	}{
		{"empty file", "", "interface is not set"},
		{"no address", "interface: eth0\nsmb: {password: x}\n", "address is not set"},
		{"unknown key", base("adress: 1.2.3.4\n"), "adress"},
		{"unknown nested key", "interface: e\naddress: 10.0.0.1\nsmb: {pasword: x}\n", "pasword"},
		{"IPv6 address", "interface: e\naddress: fe80::1\nsmb: {password: x}\n", "not an IPv4 address"},
		{"wildcard address", "interface: e\naddress: 0.0.0.0\nsmb: {password: x}\n", "cannot be reached"},
		{"loopback address", "interface: e\naddress: 127.0.0.1\nsmb: {password: x}\n", "cannot be reached"},
		{"interface with a slash", "interface: ../x\naddress: 10.0.0.1\nsmb: {password: x}\n", "not a valid interface name"},
		{"relative data dir", base("data_dir: var/kyro\n"), "absolute"},
		{"bad dhcp mode", base("dhcp: {mode: relay}\n"), "proxy, full or off"},
		{"full dhcp without a range", base("dhcp: {mode: full}\n"), "range_start"},
		{"range outside the network", base("dhcp: {mode: full, range_start: 10.0.0.1, range_end: 10.0.0.9, netmask: 255.255.255.0}\n"), "inside"},
		{"descending range", base("dhcp: {mode: full, range_start: 192.168.77.99, range_end: 192.168.77.50, netmask: 255.255.255.0}\n"), "ascending"},
		{"range with our address", base("dhcp: {mode: full, range_start: 192.168.77.1, range_end: 192.168.77.50, netmask: 255.255.255.0}\n"), "own address"},
		{"huge range", base("dhcp: {mode: full, range_start: 192.168.100.2, range_end: 192.168.200.2, netmask: 255.255.0.0}\n"), "limit"},
		{"netmask with holes", base("dhcp: {mode: full, range_start: 192.168.77.50, range_end: 192.168.77.60, netmask: 255.0.255.0}\n"), "netmask"},
		{"port zero", base("http: {port: 0}\n"), "http.port"},
		{"port too large", base("http: {port: 70000}\n"), "http.port"},
		{"no smb password", "interface: e\naddress: 10.0.0.1\n", "smb.password"},
		{"smb password with a space", "interface: e\naddress: 10.0.0.1\nsmb: {password: 'a b'}\n", "smb.password"},
		{"smb user with a quote", "interface: e\naddress: 10.0.0.1\nsmb: {username: 'a\"b', password: x}\n", "smb.username"},
		{"bad smb mode", "interface: e\naddress: 10.0.0.1\nsmb: {mode: external, password: x}\n", "embedded or off"},
		{"status without a port", base("status: {listen: localhost}\n"), "status.listen"},
		{"bad unknown_clients", base("unknown_clients: install\n"), "local or menu"},
		{"timeout too short", base("install_timeout: 1m\n"), "install_timeout"},
		{"timeout not a duration", base("install_timeout: soon\n"), "duration"},
		{"not YAML", "{{{", "yaml"},
	}
	for _, tt := range tests {
		_, err := Parse([]byte(tt.yaml))
		if err == nil {
			t.Errorf("%s: accepted", tt.name)
		} else if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error %q does not mention %q", tt.name, err, tt.want)
		}
	}
}

func TestSMBOffNeedsNoPassword(t *testing.T) {
	if _, err := Parse([]byte("interface: e\naddress: 10.0.0.1\nsmb: {mode: 'off'}\n")); err != nil {
		t.Error(err)
	}
}

func TestErrorDoesNotLeakPassword(t *testing.T) {
	_, err := Parse([]byte("interface: e\naddress: 10.0.0.1\nsmb: {password: 'top secret'}\n"))
	if err == nil || strings.Contains(err.Error(), "top secret") {
		t.Errorf("err = %v", err)
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(minimal))
	f.Add([]byte("interface: e\naddress: 10.0.0.1\ndhcp: {mode: full, range_start: 10.0.0.5, range_end: 10.0.0.9, netmask: 255.255.255.0}\nsmb: {password: x}\n"))
	f.Add([]byte("a: &a [*a]"))
	f.Fuzz(func(t *testing.T, data []byte) {
		cfg, err := Parse(data)
		if err != nil {
			return
		}
		// Whatever is accepted must be usable.
		if _, err := cfg.Addr(); err != nil {
			t.Errorf("accepted configuration has no address: %v", err)
		}
		if cfg.Validate() != nil {
			t.Error("accepted configuration does not validate")
		}
	})
}
