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

// Package config reads and checks Kyro's configuration file.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"go.yaml.in/yaml/v3"
)

// DefaultPath is where the configuration is looked for.
const DefaultPath = "/etc/kyro/config.yaml"

// Config is the content of the configuration file.
type Config struct {
	// Interface is the network interface that faces the clients.
	Interface string `yaml:"interface"`
	// Address is the IPv4 address on that interface under which clients
	// reach Kyro. Every service listens on it.
	Address string `yaml:"address"`
	// DataDir holds images, profiles, client state and boot files.
	DataDir string `yaml:"data_dir"`

	DHCP   DHCP   `yaml:"dhcp"`
	TFTP   TFTP   `yaml:"tftp"`
	HTTP   HTTP   `yaml:"http"`
	SMB    SMB    `yaml:"smb"`
	Status Status `yaml:"status"`

	// UnknownClients says what a client gets that is not armed: "local"
	// sends it to its own disk at once, "menu" shows a menu first that can
	// do nothing but that and show the client's identifiers.
	UnknownClients string `yaml:"unknown_clients"`
	// InstallTimeout is how long an installation may take before the
	// client is marked as failed.
	InstallTimeout Duration `yaml:"install_timeout"`
}

// DHCP configures the DHCP service.
type DHCP struct {
	// Mode is "proxy" (another server hands out addresses, Kyro only adds
	// the boot information), "full" (Kyro hands out addresses too) or
	// "off".
	Mode string `yaml:"mode"`
	// The following apply to mode "full" only.
	RangeStart string   `yaml:"range_start"`
	RangeEnd   string   `yaml:"range_end"`
	Netmask    string   `yaml:"netmask"`
	Router     string   `yaml:"router"`
	DNS        []string `yaml:"dns"`
	LeaseTime  Duration `yaml:"lease_time"`
}

// TFTP configures the TFTP service.
type TFTP struct {
	Enabled *bool `yaml:"enabled"`
}

// HTTP configures the HTTP service that clients boot from.
type HTTP struct {
	Port int `yaml:"port"`
}

// SMB configures the install share.
type SMB struct {
	// Mode is "embedded" or "off".
	Mode string `yaml:"mode"`
	Port int    `yaml:"port"`
	// Username and Password are not a secret: every client that boots is
	// told them. See docs/security-model.md.
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// Status configures the read-only status page.
type Status struct {
	// Listen is the address of the status page. Empty switches it off.
	Listen string `yaml:"listen"`
}

// Duration is a time.Duration that is written like "90m" in YAML.
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: %q is not a duration like 90m or 2h", n.Line, s)
	}
	*d = Duration(v)
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// Default returns the configuration that applies where the file says
// nothing. Interface, Address and the SMB password have no default.
func Default() Config {
	yes := true
	return Config{
		DataDir:        "/var/lib/kyro",
		DHCP:           DHCP{Mode: "proxy", LeaseTime: Duration(time.Hour)},
		TFTP:           TFTP{Enabled: &yes},
		HTTP:           HTTP{Port: 80},
		SMB:            SMB{Mode: "embedded", Port: 445, Username: "kyro"},
		Status:         Status{Listen: "127.0.0.1:8080"},
		UnknownClients: "local",
		InstallTimeout: Duration(3 * time.Hour),
	}
}

// Load reads and checks the file at path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	cfg, err := Parse(data)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Parse reads and checks a configuration. Keys it does not know are errors,
// so that a typing mistake does not silently leave a default in place.
func Parse(data []byte) (Config, error) {
	cfg := Default()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	// An empty file is not a mistake; the decoder reports it as io.EOF.
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

var (
	interfaceRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@-]{0,14}$`)
	credentialRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// Validate reports the first mistake in the configuration.
func (c *Config) Validate() error {
	if c.Interface == "" {
		return errors.New("interface is not set")
	}
	if !interfaceRE.MatchString(c.Interface) {
		return fmt.Errorf("interface %q is not a valid interface name", c.Interface)
	}
	addr, err := c.Addr()
	if err != nil {
		return err
	}
	if !filepath.IsAbs(c.DataDir) {
		return fmt.Errorf("data_dir %q is not an absolute path", c.DataDir)
	}

	switch c.DHCP.Mode {
	case "proxy", "off":
	case "full":
		if err := c.DHCP.validateFull(addr); err != nil {
			return err
		}
	default:
		return fmt.Errorf("dhcp.mode %q: use proxy, full or off", c.DHCP.Mode)
	}
	if err := validPort("http.port", c.HTTP.Port); err != nil {
		return err
	}

	switch c.SMB.Mode {
	case "off":
	case "embedded":
		if err := validPort("smb.port", c.SMB.Port); err != nil {
			return err
		}
		if !credentialRE.MatchString(c.SMB.Username) {
			return fmt.Errorf("smb.username %q: use 1 to 64 letters, digits, dots, underscores and hyphens", c.SMB.Username)
		}
		if !credentialRE.MatchString(c.SMB.Password) {
			return errors.New("smb.password: use 1 to 64 letters, digits, dots, underscores and hyphens")
		}
	default:
		return fmt.Errorf("smb.mode %q: use embedded or off", c.SMB.Mode)
	}

	if c.Status.Listen != "" {
		if _, err := netip.ParseAddrPort(c.Status.Listen); err != nil {
			return fmt.Errorf("status.listen %q: use ADDRESS:PORT, for example 127.0.0.1:8080", c.Status.Listen)
		}
	}
	switch c.UnknownClients {
	case "local", "menu":
	default:
		return fmt.Errorf("unknown_clients %q: use local or menu", c.UnknownClients)
	}
	if d := time.Duration(c.InstallTimeout); d < 10*time.Minute || d > 48*time.Hour {
		return fmt.Errorf("install_timeout %v: use 10m to 48h", d)
	}
	return nil
}

func validPort(name string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%s %d: use 1 to 65535", name, port)
	}
	return nil
}

func (d *DHCP) validateFull(server netip.Addr) error {
	start, err1 := parse4("dhcp.range_start", d.RangeStart)
	end, err2 := parse4("dhcp.range_end", d.RangeEnd)
	mask, err3 := parse4("dhcp.netmask", d.Netmask)
	if err := errors.Join(err1, err2, err3); err != nil {
		return fmt.Errorf("dhcp.mode full needs range_start, range_end and netmask: %w", err)
	}
	ones, ok := maskBits(mask)
	if !ok || ones < 8 || ones > 30 {
		return fmt.Errorf("dhcp.netmask %s is not a netmask between /8 and /30", mask)
	}
	prefix := netip.PrefixFrom(server, ones).Masked()
	if !prefix.Contains(start) || !prefix.Contains(end) || end.Less(start) {
		return fmt.Errorf("dhcp range %s to %s is not an ascending range inside %s", start, end, prefix)
	}
	if !server.Less(start) && !end.Less(server) {
		return fmt.Errorf("dhcp range %s to %s contains Kyro's own address %s", start, end, server)
	}
	if n := addrDiff(start, end) + 1; n > 4096 {
		return fmt.Errorf("dhcp range holds %d addresses; the limit is 4096", n)
	}
	if d.Router != "" {
		if _, err := parse4("dhcp.router", d.Router); err != nil {
			return err
		}
	}
	for _, s := range d.DNS {
		if _, err := parse4("dhcp.dns", s); err != nil {
			return err
		}
	}
	if len(d.DNS) > 4 {
		return errors.New("dhcp.dns: at most 4 servers")
	}
	if t := time.Duration(d.LeaseTime); t < time.Minute || t > 7*24*time.Hour {
		return fmt.Errorf("dhcp.lease_time %v: use 1m to 168h", t)
	}
	return nil
}

func parse4(name, s string) (netip.Addr, error) {
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() {
		return netip.Addr{}, fmt.Errorf("%s %q is not an IPv4 address", name, s)
	}
	return a, nil
}

func maskBits(mask netip.Addr) (int, bool) {
	b := mask.As4()
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	ones := 0
	for v&0x80000000 != 0 {
		ones++
		v <<= 1
	}
	return ones, v == 0
}

func addrDiff(a, b netip.Addr) int64 {
	x, y := a.As4(), b.As4()
	return int64(uint32(y[0])<<24|uint32(y[1])<<16|uint32(y[2])<<8|uint32(y[3])) -
		int64(uint32(x[0])<<24|uint32(x[1])<<16|uint32(x[2])<<8|uint32(x[3]))
}

// Addr returns Address as an IPv4 address.
func (c *Config) Addr() (netip.Addr, error) {
	if c.Address == "" {
		return netip.Addr{}, errors.New("address is not set")
	}
	a, err := parse4("address", c.Address)
	if err != nil {
		return netip.Addr{}, err
	}
	if a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() {
		return netip.Addr{}, fmt.Errorf("address %s cannot be reached by clients", a)
	}
	return a, nil
}

// TFTPEnabled reports whether the TFTP service runs.
func (c *Config) TFTPEnabled() bool { return c.TFTP.Enabled == nil || *c.TFTP.Enabled }

// TFTPEnabledValue is TFTPEnabled for a Config value instead of a pointer.
func (c Config) TFTPEnabledValue() bool { return c.TFTPEnabled() }

// HTTPBase returns the URL prefix under which clients reach the HTTP
// service, without a trailing slash.
func (c *Config) HTTPBase() string {
	if c.HTTP.Port == 80 {
		return "http://" + c.Address
	}
	return fmt.Sprintf("http://%s:%d", c.Address, c.HTTP.Port)
}

// ImagesDir is where the ISOs are kept.
func (c *Config) ImagesDir() string { return filepath.Join(c.DataDir, "images") }

// ProfilesDir is where the profiles are kept.
func (c *Config) ProfilesDir() string { return filepath.Join(c.DataDir, "profiles") }

// StateDir is where the client records are kept. It is the only directory
// that the service writes to.
func (c *Config) StateDir() string { return filepath.Join(c.DataDir, "state") }

// BootDir is where iPXE and wimboot are kept.
func (c *Config) BootDir() string { return filepath.Join(c.DataDir, "boot") }
