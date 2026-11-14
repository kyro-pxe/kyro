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

package doctor

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/kyro-pxe/kyro/internal/config"
)

// fake is a machine that the tests describe.
type fake struct {
	commands map[string]string // "ufw status" -> output; missing means not installed
	inUse    map[string]error  // "udp/67" -> error from binding
	addrs    []string
	up       bool
	noIface  bool
	free     int64
	euid     int
	kyroUser bool
	kyroRuns bool
}

func (f *fake) system() System {
	bind := func(proto string) func(string) error {
		return func(addr string) error {
			port := addr[strings.LastIndex(addr, ":")+1:]
			return f.inUse[proto+"/"+port]
		}
	}
	return System{
		Run: func(name string, args ...string) (string, error) {
			key := strings.TrimSpace(name + " " + strings.Join(args, " "))
			if out, ok := f.commands[key]; ok {
				return out, nil
			}
			// A program is installed if any command of it is described.
			for k := range f.commands {
				if strings.HasPrefix(k, name+" ") {
					return "", errors.New("exit status 1")
				}
			}
			return "", fmt.Errorf("%w: %s", exec.ErrNotFound, name)
		},
		BindUDP: bind("udp"),
		BindTCP: bind("tcp"),
		Interface: func(string) ([]string, bool, error) {
			if f.noIface {
				return nil, false, errors.New("no such network interface")
			}
			return f.addrs, f.up, nil
		},
		FreeBytes:   func(string) (int64, bool) { return f.free, true },
		UserExists:  func(string) bool { return f.kyroUser },
		KyroRunning: func() bool { return f.kyroRuns },
		Euid:        f.euid,
	}
}

func testConfig(t *testing.T) config.Config {
	cfg := config.Default()
	cfg.Interface, cfg.Address, cfg.DataDir, cfg.SMB.Password = "eth1", "192.168.77.1", t.TempDir(), "x"
	return cfg
}

func healthy() *fake {
	return &fake{
		commands: map[string]string{"systemctl --version": "systemd 257"},
		addrs:    []string{"192.168.77.1"},
		up:       true,
		free:     100 << 30,
	}
}

func report(t *testing.T, f *fake, cfg config.Config) (string, int, int) {
	t.Helper()
	var buf bytes.Buffer
	warnings, failures := Print(&buf, Check(f.system(), "/etc/kyro/config.yaml", cfg, nil))
	return buf.String(), warnings, failures
}

func wants(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("report lacks %q:\n%s", w, got)
		}
	}
}

func TestHealthyMachine(t *testing.T) {
	out, warnings, failures := report(t, healthy(), testConfig(t))
	// The boot files are missing in a fresh data directory: one failure,
	// with the command that fixes it.
	if warnings != 0 || failures != 1 {
		t.Errorf("%d warnings, %d failures:\n%s", warnings, failures, out)
	}
	wants(t, out,
		"[ok  ] Configuration",
		"[ok  ] Boot interface: eth1 is up and has the address 192.168.77.1.",
		"[ok  ] Ports: Free: UDP port 67 (DHCP), UDP port 4011 (PXE boot server), UDP port 69 (TFTP), TCP port 80 (HTTP for booting clients), TCP port 445 (SMB install share).",
		"[ok  ] Other services: No Samba, DHCP or TFTP service is active",
		"[ok  ] Firewall: No active ufw, firewalld or nftables",
		"[FAIL] iPXE and wimboot",
		"kyro bootfiles fetch",
		"[info] Images: None yet.",
		"kyro image add",
		"[info] Profiles: None yet.",
		"[ok  ] Disk space for images: 100 GB free",
		"[info] Secure Boot: Not supported.",
		"selection, not authentication",
		"6 GB",
	)
}

func TestInterfaceProblems(t *testing.T) {
	f := healthy()
	f.noIface = true
	out, _, _ := report(t, f, testConfig(t))
	wants(t, out, "[FAIL] Boot interface: Interface eth1: no such network interface", "ip -br addr")

	f = healthy()
	f.up = false
	out, _, _ = report(t, f, testConfig(t))
	wants(t, out, "[FAIL] Boot interface: Interface eth1 is down.", "ip link set eth1 up")

	f = healthy()
	f.addrs = []string{"10.0.0.5", "10.0.0.6"}
	out, _, _ = report(t, f, testConfig(t))
	wants(t, out, "[FAIL] Boot interface: eth1 does not have the address 192.168.77.1. Its addresses: 10.0.0.5, 10.0.0.6.")
}

func TestPortsInUse(t *testing.T) {
	f := healthy()
	f.inUse = map[string]error{
		"udp/67":  syscall.EADDRINUSE,
		"tcp/445": syscall.EADDRINUSE,
		"tcp/80":  syscall.EADDRINUSE,
		"udp/69":  syscall.EADDRINUSE,
	}
	f.commands["ss -lnpuH"] = `UNCONN 0 0 0.0.0.0:67 0.0.0.0:* users:(("dnsmasq",pid=812,fd=4))
UNCONN 0 0 192.168.77.1:69 0.0.0.0:* users:(("kyro",pid=900,fd=9))
`
	f.commands["ss -lnptH"] = `LISTEN 0 50 0.0.0.0:445 0.0.0.0:* users:(("smbd",pid=700,fd=30))
LISTEN 0 511 0.0.0.0:80 0.0.0.0:*
`
	out, _, failures := report(t, f, testConfig(t))
	if failures != 4 { // three ports and the boot files
		t.Errorf("%d failures:\n%s", failures, out)
	}
	wants(t, out,
		"[FAIL] UDP port 67 (DHCP): In use by dnsmasq.",
		"[FAIL] TCP port 445 (SMB install share): In use by smbd.",
		"smb.port",
		"[FAIL] TCP port 80 (HTTP for booting clients): In use by another program.",
		"ss -lnpt | grep ':80 '",
		"Kyro is running and listens on: UDP port 69 (TFTP).",
		"Free: UDP port 4011 (PXE boot server).",
	)
}

// In a container without ss, a port that is taken cannot be attributed. If
// Kyro runs there, that is no failure; if it does not, it is.
func TestPortsInUseWithoutSS(t *testing.T) {
	for _, runs := range []bool{true, false} {
		f := healthy()
		delete(f.commands, "ss -lnpuH")
		delete(f.commands, "ss -lnptH")
		f.inUse = map[string]error{"udp/67": syscall.EADDRINUSE, "tcp/80": syscall.EADDRINUSE}
		f.kyroRuns = runs
		out, _, _ := report(t, f, testConfig(t))
		if runs {
			wants(t, out, "[info] Ports: In use, and Kyro is running here: UDP port 67 (DHCP), TCP port 80 (HTTP for booting clients).")
			if strings.Contains(out, "[FAIL] UDP port 67") {
				t.Errorf("a port held while Kyro runs is a failure:\n%s", out)
			}
		} else {
			wants(t, out, "[FAIL] UDP port 67 (DHCP): In use by another program.", "[FAIL] TCP port 80")
		}
	}
}

func TestPortsWithoutPrivileges(t *testing.T) {
	f := healthy()
	f.euid = 1000
	f.inUse = map[string]error{"udp/67": syscall.EACCES, "udp/69": syscall.EACCES, "tcp/80": syscall.EACCES, "tcp/445": syscall.EACCES}
	out, _, _ := report(t, f, testConfig(t))
	wants(t, out, "[info] UDP port 67 (DHCP): Not checked: this user may not open ports below 1024. Run 'kyro doctor' as root")
	if strings.Contains(out, "[FAIL] UDP port") || strings.Contains(out, "[FAIL] TCP port") {
		t.Errorf("a port that could not be checked is reported as failed:\n%s", out)
	}
}

func TestOnlyConfiguredPortsAreChecked(t *testing.T) {
	cfg := testConfig(t)
	off := false
	cfg.DHCP.Mode, cfg.TFTP.Enabled, cfg.SMB.Mode, cfg.HTTP.Port = "off", &off, "off", 8080
	f := healthy()
	f.inUse = map[string]error{"udp/67": syscall.EADDRINUSE, "udp/69": syscall.EADDRINUSE, "tcp/445": syscall.EADDRINUSE, "tcp/80": syscall.EADDRINUSE}
	f.commands["systemctl is-active smbd"] = "active\n"
	f.commands["systemctl is-active dnsmasq"] = "active\n"
	out, _, _ := report(t, f, cfg)
	wants(t, out, "Free: TCP port 8080 (HTTP for booting clients).")
	for _, absent := range []string{"port 67", "port 69", "port 445", "port 80 ", "Service smbd", "Service dnsmasq"} {
		if strings.Contains(out, absent) {
			t.Errorf("report mentions %q although that part is switched off:\n%s", absent, out)
		}
	}
}

func TestConflictingServices(t *testing.T) {
	f := healthy()
	f.commands["systemctl is-active smbd"] = "active\n"
	f.commands["systemctl is-active dnsmasq"] = "active\n"
	f.commands["systemctl is-active tftpd-hpa"] = "active\n"
	f.commands["systemctl is-active systemd-resolved"] = "active\n"
	f.commands["systemctl is-active isc-dhcp-server"] = "inactive\n"
	out, warnings, _ := report(t, f, testConfig(t))
	if warnings != 3 {
		t.Errorf("%d warnings:\n%s", warnings, out)
	}
	wants(t, out,
		"[WARN] Service smbd: Active. It is Samba, which listens on port 445.",
		"systemctl disable --now smbd",
		"[WARN] Service dnsmasq",
		"[WARN] Service tftpd-hpa",
		"[info] Service systemd-resolved: Active. It uses port 53, which Kyro does not need. No conflict.",
	)
	if strings.Contains(out, "isc-dhcp-server") {
		t.Errorf("an inactive service is reported:\n%s", out)
	}

	// Without systemd nothing is claimed.
	f = healthy()
	delete(f.commands, "systemctl --version")
	out, _, _ = report(t, f, testConfig(t))
	wants(t, out, "[info] Other services: Not checked: systemctl is not available.")
}

func TestFirewalls(t *testing.T) {
	cfg := testConfig(t)

	f := healthy()
	f.commands["ufw status"] = "Status: active\n\nTo Action From\n"
	out, warnings, _ := report(t, f, cfg)
	wants(t, out,
		"[WARN] Firewall (ufw): ufw is active. Kyro cannot tell whether its ports are open in it, and does not change it.",
		"ufw allow in on eth1 to any port 67 proto udp",
		"ufw allow in on eth1 to any port 4011 proto udp",
		"ufw allow in on eth1 to any port 69 proto udp",
		"ufw allow in on eth1 to any port 80 proto tcp",
		"ufw allow in on eth1 to any port 445 proto tcp",
	)
	if warnings != 1 {
		t.Errorf("%d warnings", warnings)
	}

	f = healthy()
	f.commands["ufw status"] = "Status: inactive\n"
	f.commands["firewall-cmd --state"] = "running\n"
	f.commands["firewall-cmd --get-zone-of-interface=eth1"] = "internal\n"
	out, _, _ = report(t, f, cfg)
	wants(t, out,
		"[WARN] Firewall (firewalld): firewalld is running; eth1 is in zone internal.",
		"firewall-cmd --permanent --zone=internal --add-port=67/udp",
		"firewall-cmd --permanent --zone=internal --add-port=445/tcp",
		"firewall-cmd --reload",
	)

	f = healthy()
	f.commands["nft list ruleset"] = "table inet filter {\n chain input {\n  type filter hook input priority filter; policy drop;\n }\n}\n"
	out, _, _ = report(t, f, cfg)
	wants(t, out,
		"[WARN] Firewall (nftables)",
		`nft add rule inet filter input iifname "eth1" udp dport { 67, 4011, 69 } accept`,
		`nft add rule inet filter input iifname "eth1" tcp dport { 80, 445 } accept`,
	)

	// A ruleset without an input hook, as Docker leaves it, is not a
	// firewall in Kyro's way.
	f = healthy()
	f.commands["nft list ruleset"] = "table ip nat {\n chain POSTROUTING {\n  type nat hook postrouting priority srcnat;\n }\n}\n"
	out, _, _ = report(t, f, cfg)
	wants(t, out, "[ok  ] Firewall: No active ufw")
}

func TestNoConfiguration(t *testing.T) {
	var buf bytes.Buffer
	_, failures := Print(&buf, Check(healthy().system(), "/etc/kyro/config.yaml", config.Config{}, os.ErrNotExist))
	out := buf.String()
	if failures != 1 {
		t.Errorf("%d failures", failures)
	}
	wants(t, out, "[FAIL] Configuration: There is no configuration at /etc/kyro/config.yaml.", "install.sh", "selection, not authentication")

	buf.Reset()
	Print(&buf, Check(healthy().system(), "/etc/kyro/config.yaml", config.Config{}, errors.New("line 3: field adress not found")))
	wants(t, buf.String(), "[FAIL] Configuration: line 3: field adress not found")
}

func TestDataChecks(t *testing.T) {
	cfg := testConfig(t)
	f := healthy()
	f.free = 3 << 30
	out, _, _ := report(t, f, cfg)
	wants(t, out, "[WARN] Disk space for images: 3 GB free", "--link")

	// A damaged store is a failure, not "none".
	os.MkdirAll(filepath.Join(cfg.ProfilesDir(), "broken"), 0o755)
	os.WriteFile(filepath.Join(cfg.ProfilesDir(), "broken", "profile.json"), []byte("{"), 0o644)
	out, _, _ = report(t, healthy(), cfg)
	wants(t, out, "[FAIL] Profiles")
}

func TestDoctorChangesNothing(t *testing.T) {
	// Every command the checks run only asks.
	f := healthy()
	var ran []string
	sys := f.system()
	inner := sys.Run
	sys.Run = func(name string, args ...string) (string, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return inner(name, args...)
	}
	Check(sys, "x", testConfig(t), nil)
	for _, cmd := range ran {
		for _, verb := range []string{"allow", "add", "--permanent", "--reload", "disable", "stop", "start", "enable", "delete", "flush", "insert"} {
			if strings.Contains(" "+cmd+" ", " "+verb+" ") || strings.Contains(cmd, verb+"-") {
				t.Errorf("doctor ran a command that changes something: %s", cmd)
			}
		}
	}
	if len(ran) == 0 {
		t.Error("no commands were run")
	}
}
