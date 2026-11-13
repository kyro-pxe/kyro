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

// Package doctor checks whether a machine is ready to run Kyro, and says
// what to do about what it finds.
//
// It only looks. It never changes a firewall, stops a service or edits a
// file.
package doctor

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/kyro-pxe/kyro/internal/bootfiles"
	"github.com/kyro-pxe/kyro/internal/config"
	"github.com/kyro-pxe/kyro/internal/images"
	"github.com/kyro-pxe/kyro/internal/profiles"
)

// Level says how serious a finding is.
type Level int

// The levels of a finding.
const (
	OK   Level = iota // as it should be
	Info              // worth knowing, nothing to do
	Warn              // will probably cause trouble
	Fail              // Kyro will not work like this
)

func (l Level) String() string {
	return [...]string{"ok", "info", "WARN", "FAIL"}[l]
}

// Finding is the result of one check.
type Finding struct {
	Level Level
	Title string
	// Detail explains the finding, and Fix lists commands or steps that
	// would resolve it. Both may be empty.
	Detail string
	Fix    []string
}

// System is what the checks ask the machine. Tests replace it.
type System struct {
	// Run runs a program and returns what it printed. A program that is
	// not installed gives an error that wraps exec.ErrNotFound.
	Run func(name string, args ...string) (string, error)
	// BindUDP and BindTCP try to open a port and close it again.
	BindUDP func(addr string) error
	BindTCP func(addr string) error
	// Interface returns the IPv4 addresses of an interface and whether it
	// is up.
	Interface func(name string) (addrs []string, up bool, err error)
	// FreeBytes returns the free space for a directory.
	FreeBytes func(dir string) (int64, bool)
	// UserExists reports whether the machine has a user of that name.
	UserExists func(name string) bool
	// Euid is the effective user ID.
	Euid int
}

// Host returns the System of the machine the program runs on.
func Host() System {
	return System{
		Run: func(name string, args ...string) (string, error) {
			out, err := exec.Command(name, args...).CombinedOutput()
			return string(out), err
		},
		BindUDP: func(addr string) error {
			c, err := net.ListenPacket("udp4", addr)
			if err == nil {
				c.Close()
			}
			return err
		},
		BindTCP: func(addr string) error {
			l, err := net.Listen("tcp4", addr)
			if err == nil {
				l.Close()
			}
			return err
		},
		Interface: func(name string) ([]string, bool, error) {
			ifi, err := net.InterfaceByName(name)
			if err != nil {
				return nil, false, err
			}
			addrs, err := ifi.Addrs()
			if err != nil {
				return nil, false, err
			}
			var out []string
			for _, a := range addrs {
				if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
					out = append(out, n.IP.String())
				}
			}
			return out, ifi.Flags&net.FlagUp != 0, nil
		},
		FreeBytes: freeBytes,
		UserExists: func(name string) bool {
			_, err := user.Lookup(name)
			return err == nil
		},
		Euid: os.Geteuid(),
	}
}

// port is one port that Kyro listens on.
type port struct {
	proto string // "udp" or "tcp"
	addr  string // address to bind for the check
	num   int
	what  string
}

func ports(cfg config.Config) []port {
	var out []port
	if cfg.DHCP.Mode != "off" {
		out = append(out,
			port{"udp", "0.0.0.0", 67, "DHCP"},
			port{"udp", cfg.Address, 4011, "PXE boot server"})
	}
	if cfg.TFTPEnabled() {
		out = append(out, port{"udp", cfg.Address, 69, "TFTP"})
	}
	out = append(out, port{"tcp", cfg.Address, cfg.HTTP.Port, "HTTP for booting clients"})
	if cfg.SMB.Mode == "embedded" {
		out = append(out, port{"tcp", cfg.Address, cfg.SMB.Port, "SMB install share"})
	}
	return out
}

// Check runs every check. cfgErr is the error from loading the
// configuration at cfgPath, if there was one; most checks need a
// configuration and are skipped without it.
func Check(sys System, cfgPath string, cfg config.Config, cfgErr error) []Finding {
	var out []Finding
	add := func(f ...Finding) { out = append(out, f...) }

	if cfgErr != nil {
		f := Finding{Level: Fail, Title: "Configuration", Detail: cfgErr.Error()}
		if errors.Is(cfgErr, os.ErrNotExist) {
			f.Detail = "There is no configuration at " + cfgPath + "."
			f.Fix = []string{"Run install.sh, or write the file by hand; the README has an example."}
		}
		add(f)
		add(notes()...)
		return out
	}
	add(Finding{Level: OK, Title: "Configuration", Detail: cfgPath + " is valid."})
	add(checkInterface(sys, cfg))
	add(checkPorts(sys, cfg)...)
	add(checkServices(sys, cfg)...)
	add(checkFirewall(sys, cfg)...)
	add(checkBootFiles(cfg))
	add(checkData(sys, cfg)...)
	add(notes()...)
	return out
}

func checkInterface(sys System, cfg config.Config) Finding {
	addrs, up, err := sys.Interface(cfg.Interface)
	switch {
	case err != nil:
		return Finding{Level: Fail, Title: "Boot interface", Detail: fmt.Sprintf("Interface %s: %v.", cfg.Interface, err),
			Fix: []string{"Set 'interface' in the configuration to the interface that faces the clients ('ip -br addr' lists them)."}}
	case !up:
		return Finding{Level: Fail, Title: "Boot interface", Detail: "Interface " + cfg.Interface + " is down.",
			Fix: []string{"ip link set " + cfg.Interface + " up"}}
	}
	for _, a := range addrs {
		if a == cfg.Address {
			return Finding{Level: OK, Title: "Boot interface", Detail: fmt.Sprintf("%s is up and has the address %s.", cfg.Interface, cfg.Address)}
		}
	}
	return Finding{Level: Fail, Title: "Boot interface",
		Detail: fmt.Sprintf("%s does not have the address %s. Its addresses: %s.", cfg.Interface, cfg.Address, orNone(addrs)),
		Fix:    []string{"Set 'address' in the configuration to one of the interface's addresses, or add the address to the interface."}}
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

// listeners returns which program listens on each port, as far as the
// program "ss" can tell: "udp/67" -> "dnsmasq".
func listeners(sys System) map[string]string {
	out := map[string]string{}
	for _, proto := range []string{"udp", "tcp"} {
		flag := "-lnpuH"
		if proto == "tcp" {
			flag = "-lnptH"
		}
		text, err := sys.Run("ss", flag)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(text, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 5 {
				continue
			}
			// Local address is the fourth or fifth column, depending on
			// the protocol; it is the first that ends in ":port".
			local := ""
			for _, f := range fields[2:] {
				if i := strings.LastIndex(f, ":"); i >= 0 && isNumber(f[i+1:]) {
					local = f
					break
				}
			}
			if local == "" {
				continue
			}
			num := local[strings.LastIndex(local, ":")+1:]
			who := "another program"
			if i := strings.Index(line, `users:(("`); i >= 0 {
				rest := line[i+len(`users:(("`):]
				if j := strings.Index(rest, `"`); j > 0 {
					who = rest[:j]
				}
			}
			key := proto + "/" + num
			if out[key] == "" || who == "kyro" {
				out[key] = who
			}
		}
	}
	return out
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func checkPorts(sys System, cfg config.Config) []Finding {
	who := listeners(sys)
	var out []Finding
	var free, ours []string
	for _, p := range ports(cfg) {
		name := fmt.Sprintf("%s port %d (%s)", strings.ToUpper(p.proto), p.num, p.what)
		addr := net.JoinHostPort(p.addr, fmt.Sprint(p.num))
		var err error
		if p.proto == "udp" {
			err = sys.BindUDP(addr)
		} else {
			err = sys.BindTCP(addr)
		}
		holder := who[fmt.Sprintf("%s/%d", p.proto, p.num)]
		switch {
		case err == nil:
			free = append(free, name)
		case errors.Is(err, syscall.EADDRINUSE) && holder == "kyro":
			ours = append(ours, name)
		case errors.Is(err, syscall.EADDRINUSE):
			if holder == "" {
				holder = "another program"
			}
			f := Finding{Level: Fail, Title: name, Detail: "In use by " + holder + ".",
				Fix: []string{"See which program it is: ss -lnp" + map[string]string{"udp": "u", "tcp": "t"}[p.proto] + " | grep ':" + fmt.Sprint(p.num) + " '", "Stop that program, or give Kyro an address of its own on the boot interface."}}
			if p.num == 445 {
				f.Fix = append(f.Fix, "Or move the install share to another port (smb.port). Only Windows 11 24H2 and later can install from one.")
			}
			out = append(out, f)
		case errors.Is(err, syscall.EACCES):
			out = append(out, Finding{Level: Info, Title: name, Detail: "Not checked: this user may not open ports below 1024. Run 'kyro doctor' as root to check."})
		case errors.Is(err, syscall.EADDRNOTAVAIL):
			// The interface check has reported the missing address.
		default:
			out = append(out, Finding{Level: Warn, Title: name, Detail: "Could not be opened: " + err.Error()})
		}
	}
	if len(ours) > 0 {
		out = append(out, Finding{Level: OK, Title: "Ports", Detail: "Kyro is running and listens on: " + strings.Join(ours, ", ") + "."})
	}
	if len(free) > 0 {
		out = append(out, Finding{Level: OK, Title: "Ports", Detail: "Free: " + strings.Join(free, ", ") + "."})
	}
	return out
}

// service is a systemd unit that gets in Kyro's way.
type service struct {
	unit string
	what string
	// needs says which part of Kyro's configuration makes it a conflict.
	needs func(config.Config) bool
}

var dhcpOn = func(c config.Config) bool { return c.DHCP.Mode != "off" }

var services = []service{
	{"smbd", "Samba, which listens on port 445", func(c config.Config) bool { return c.SMB.Mode == "embedded" && c.SMB.Port == 445 }},
	{"samba", "Samba, which listens on port 445", func(c config.Config) bool { return c.SMB.Mode == "embedded" && c.SMB.Port == 445 }},
	{"samba-ad-dc", "Samba, which listens on port 445", func(c config.Config) bool { return c.SMB.Mode == "embedded" && c.SMB.Port == 445 }},
	{"ksmbd", "the kernel SMB server, which listens on port 445", func(c config.Config) bool { return c.SMB.Mode == "embedded" && c.SMB.Port == 445 }},
	{"dnsmasq", "dnsmasq, which usually answers DHCP on port 67 and can serve TFTP", dhcpOn},
	{"isc-dhcp-server", "a DHCP server on port 67", dhcpOn},
	{"dhcpd", "a DHCP server on port 67", dhcpOn},
	{"kea-dhcp4-server", "a DHCP server on port 67", dhcpOn},
	{"kea-dhcp4", "a DHCP server on port 67", dhcpOn},
	{"tftpd-hpa", "a TFTP server on port 69", config.Config.TFTPEnabledValue},
	{"atftpd", "a TFTP server on port 69", config.Config.TFTPEnabledValue},
	{"tftp.socket", "a TFTP server on port 69", config.Config.TFTPEnabledValue},
}

func checkServices(sys System, cfg config.Config) []Finding {
	if _, err := sys.Run("systemctl", "--version"); err != nil {
		return []Finding{{Level: Info, Title: "Other services", Detail: "Not checked: systemctl is not available."}}
	}
	active := func(unit string) bool {
		out, _ := sys.Run("systemctl", "is-active", unit)
		return strings.TrimSpace(out) == "active"
	}
	var out []Finding
	for _, s := range services {
		if !s.needs(cfg) || !active(s.unit) {
			continue
		}
		out = append(out, Finding{
			Level:  Warn,
			Title:  "Service " + s.unit,
			Detail: "Active. It is " + s.what + ". If it listens on the boot interface, it and Kyro cannot both run. If it serves another interface only, this is fine; the port checks above tell.",
			Fix:    []string{"To stop it for good: systemctl disable --now " + s.unit},
		})
	}
	// The DHCP server that hands out addresses is expected in proxy mode,
	// but not on this machine's boot interface.
	if active("systemd-resolved") {
		out = append(out, Finding{Level: Info, Title: "Service systemd-resolved", Detail: "Active. It uses port 53, which Kyro does not need. No conflict."})
	}
	if len(out) == 0 || (len(out) == 1 && out[0].Level == Info) {
		out = append([]Finding{{Level: OK, Title: "Other services", Detail: "No Samba, DHCP or TFTP service is active on this machine."}}, out...)
	}
	return out
}

// checkFirewall says which firewall is active and what would open Kyro's
// ports in it. It cannot tell whether they are open already.
func checkFirewall(sys System, cfg config.Config) []Finding {
	var udp, tcp []string
	for _, p := range ports(cfg) {
		if p.proto == "udp" {
			udp = append(udp, fmt.Sprint(p.num))
		} else {
			tcp = append(tcp, fmt.Sprint(p.num))
		}
	}
	const cannotTell = " Kyro cannot tell whether its ports are open in it, and does not change it. If clients get no answer, these commands open the ports on the boot interface:"

	if out, err := sys.Run("ufw", "status"); err == nil && strings.Contains(out, "Status: active") {
		var fix []string
		for _, p := range udp {
			fix = append(fix, fmt.Sprintf("ufw allow in on %s to any port %s proto udp", cfg.Interface, p))
		}
		for _, p := range tcp {
			fix = append(fix, fmt.Sprintf("ufw allow in on %s to any port %s proto tcp", cfg.Interface, p))
		}
		return []Finding{{Level: Warn, Title: "Firewall (ufw)", Detail: "ufw is active." + cannotTell, Fix: fix}}
	}
	if out, err := sys.Run("firewall-cmd", "--state"); err == nil && strings.TrimSpace(out) == "running" {
		zone := "public"
		if z, err := sys.Run("firewall-cmd", "--get-zone-of-interface="+cfg.Interface); err == nil && isWord(strings.TrimSpace(z)) {
			zone = strings.TrimSpace(z)
		}
		var fix []string
		for _, p := range udp {
			fix = append(fix, fmt.Sprintf("firewall-cmd --permanent --zone=%s --add-port=%s/udp", zone, p))
		}
		for _, p := range tcp {
			fix = append(fix, fmt.Sprintf("firewall-cmd --permanent --zone=%s --add-port=%s/tcp", zone, p))
		}
		fix = append(fix, "firewall-cmd --reload")
		return []Finding{{Level: Warn, Title: "Firewall (firewalld)", Detail: fmt.Sprintf("firewalld is running; %s is in zone %s.", cfg.Interface, zone) + cannotTell, Fix: fix}}
	}
	if out, err := sys.Run("nft", "list", "ruleset"); err == nil && strings.Contains(out, "hook input") {
		fix := []string{}
		if len(udp) > 0 {
			fix = append(fix, fmt.Sprintf(`nft add rule inet filter input iifname "%s" udp dport { %s } accept`, cfg.Interface, strings.Join(udp, ", ")))
		}
		fix = append(fix,
			fmt.Sprintf(`nft add rule inet filter input iifname "%s" tcp dport { %s } accept`, cfg.Interface, strings.Join(tcp, ", ")),
			"(Table and chain are called 'inet filter input' on most systems. 'nft list ruleset' shows yours. Make the rules permanent in /etc/nftables.conf.)")
		return []Finding{{Level: Warn, Title: "Firewall (nftables)", Detail: "There are nftables rules for incoming traffic." + cannotTell, Fix: fix}}
	}
	return []Finding{{Level: OK, Title: "Firewall", Detail: "No active ufw, firewalld or nftables input rules found."}}
}

func isWord(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func checkBootFiles(cfg config.Config) Finding {
	problems := bootfiles.Verify(cfg.BootDir(), bootfiles.Files)
	if len(problems) == 0 {
		return Finding{Level: OK, Title: "iPXE and wimboot", Detail: "Present in " + cfg.BootDir() + " with the pinned checksums (" + bootfiles.Versions + ")."}
	}
	var parts []string
	for _, p := range problems {
		parts = append(parts, p.File+": "+p.Err.Error())
	}
	return Finding{Level: Fail, Title: "iPXE and wimboot", Detail: strings.Join(parts, "; ") + ".", Fix: []string{"kyro bootfiles fetch"}}
}

func checkData(sys System, cfg config.Config) []Finding {
	var out []Finding
	imgs, err := images.Open(cfg.ImagesDir()).List()
	switch {
	case err != nil:
		out = append(out, Finding{Level: Fail, Title: "Images", Detail: err.Error()})
	case len(imgs) == 0:
		out = append(out, Finding{Level: Info, Title: "Images", Detail: "None yet.", Fix: []string{"kyro image add /path/to/windows11.iso"}})
	default:
		names := make([]string, len(imgs))
		for i, im := range imgs {
			names[i] = im.Name
		}
		out = append(out, Finding{Level: OK, Title: "Images", Detail: strings.Join(names, ", ") + "."})
	}
	profs, err := profiles.Open(cfg.ProfilesDir()).List()
	switch {
	case err != nil:
		out = append(out, Finding{Level: Fail, Title: "Profiles", Detail: err.Error()})
	case len(profs) == 0:
		out = append(out, Finding{Level: Info, Title: "Profiles", Detail: "None yet.", Fix: []string{"kyro profile import /path/to/profile", "kyro profile import --example   (a test fixture that wipes disk 0)"}})
	default:
		names := make([]string, len(profs))
		for i, p := range profs {
			names[i] = p.Name
		}
		sort.Strings(names)
		out = append(out, Finding{Level: OK, Title: "Profiles", Detail: strings.Join(names, ", ") + "."})
	}

	if free, ok := sys.FreeBytes(cfg.ImagesDir()); ok {
		gb := float64(free) / (1 << 30)
		f := Finding{Level: OK, Title: "Disk space for images", Detail: fmt.Sprintf("%.0f GB free in %s. A Windows 11 ISO takes 5 to 8 GB.", gb, cfg.ImagesDir())}
		if len(imgs) == 0 && free < 10<<30 {
			f.Level = Warn
			f.Fix = []string{"Free some space, move data_dir to a larger disk, or add ISOs with --link so that they are not copied."}
		}
		out = append(out, f)
	}

	// The service writes to the state directory and nowhere else.
	if _, err := os.Stat(cfg.StateDir()); err == nil {
		// Only where install.sh has made the service user: there the
		// service does not run as root, and a directory that root made
		// by running the CLI first is closed to it.
		if sys.UserExists("kyro") && rootOwned(cfg.StateDir()) {
			out = append(out, Finding{Level: Warn, Title: "Client records", Detail: cfg.StateDir() + " belongs to root, and the service runs as the user kyro. It cannot record clients there, so nobody gets installed.", Fix: []string{"chown -R kyro:kyro " + cfg.StateDir()}})
		}
	} else if errors.Is(err, os.ErrNotExist) {
		out = append(out, Finding{Level: Info, Title: "Client records", Detail: filepath.Clean(cfg.StateDir()) + " does not exist yet; it is created on first use."})
	}
	return out
}

// notes are the things that are true of every installation and that an
// administrator should have read once.
func notes() []Finding {
	return []Finding{
		{Level: Info, Title: "Memory of clients", Detail: "Windows PE runs from RAM: the client loads boot.wim (about 600 MB for Windows 11) into memory before anything else. Windows 11 itself asks for 4 GB. Kyro's own test installs with 6 GB; less was not tried."},
		{Level: Info, Title: "Secure Boot", Detail: "Not supported. Switch it off on clients for the installation."},
		{Level: Info, Title: "Who gets installed", Detail: "A client is selected by the MAC address, UUID or serial number that it reports. That is selection, not authentication: any machine on the boot network can claim to be an armed client, and then receives the installer and the answer file. The same network can read the install share. Keep the boot network isolated."},
	}
}

// Print writes the findings and returns how many warnings and failures
// there were.
func Print(w io.Writer, findings []Finding) (warnings, failures int) {
	for _, f := range findings {
		fmt.Fprintf(w, "[%-4s] %s", f.Level, f.Title)
		if f.Detail != "" {
			fmt.Fprintf(w, ": %s", f.Detail)
		}
		fmt.Fprintln(w)
		for _, fix := range f.Fix {
			fmt.Fprintf(w, "         %s\n", fix)
		}
		switch f.Level {
		case Warn:
			warnings++
		case Fail:
			failures++
		}
	}
	return warnings, failures
}
