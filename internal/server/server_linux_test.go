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

//go:build linux

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jfjallid/go-smb/smb"
	"github.com/jfjallid/go-smb/spnego"

	"github.com/kyro-pxe/kyro/internal/boot"
	"github.com/kyro-pxe/kyro/internal/bootfiles"
	"github.com/kyro-pxe/kyro/internal/config"
	"github.com/kyro-pxe/kyro/internal/images"
	"github.com/kyro-pxe/kyro/internal/images/imagetest"
	"github.com/kyro-pxe/kyro/internal/profiles"
	"github.com/kyro-pxe/kyro/internal/profiles/example"
	"github.com/kyro-pxe/kyro/internal/state"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// standIns writes pretend boot files and returns their pins.
func standIns(t *testing.T, dir string) []bootfiles.File {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var files []bootfiles.File
	for _, name := range []string{"wimboot", "ipxe.efi", "undionly.kpxe"} {
		data := []byte("pretend " + name)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, bootfiles.File{Name: name, SHA256: sha(data)})
	}
	return files
}

// running is a server on loopback with one image and the example profile.
type running struct {
	cfg    config.Config
	state  *state.Store
	stop   func() error
	status string
}

func run(t *testing.T, edit func(*config.Config)) *running {
	t.Helper()
	cfg := config.Default()
	cfg.Interface, cfg.Address, cfg.DataDir = "lo", "127.0.0.1", t.TempDir()
	cfg.HTTP.Port, cfg.SMB.Port, cfg.SMB.Password = freePort(t), freePort(t), "not-a-secret"
	cfg.Status.Listen = "127.0.0.1:" + itoa(freePort(t))
	// Ports 67 and 69 need privileges; those services have their own tests.
	off := false
	cfg.DHCP.Mode, cfg.TFTP.Enabled = "off", &off
	if edit != nil {
		edit(&cfg)
	}
	files := standIns(t, cfg.BootDir())
	if _, err := images.Open(cfg.ImagesDir()).Add(imagetest.Write(t), "win11", images.AddOptions{}); err != nil {
		t.Fatal(err)
	}
	imp, err := profiles.Parse(example.Bundle(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := profiles.Open(cfg.ProfilesDir()).Add(imp, false); err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(cfg.StateDir())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() {
		done <- Run(ctx, cfg, log, Options{BootFiles: files, SkipInterfaceCheck: true, Ready: func() { close(ready) }})
	}()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("Run: %v", err)
	}
	r := &running{cfg: cfg, state: st, status: "http://" + cfg.Status.Listen}
	var stopped bool
	var stopErr error
	r.stop = func() error {
		if stopped {
			return stopErr
		}
		stopped = true
		cancel()
		select {
		case stopErr = <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the server did not stop")
		}
		return stopErr
	}
	t.Cleanup(func() { r.stop() })
	return r
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func get(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// TestWholeInstallation plays a client through an installation against all
// services at once: boot request, the files of the installation, the
// install image over SMB, the callback, and what the status page says.
func TestWholeInstallation(t *testing.T) {
	r := run(t, nil)
	base := r.cfg.HTTPBase()

	key, _ := state.ParseKey("52:54:00:77:00:01")
	armed, err := r.state.Arm(key, "win11", example.Name, false)
	if err != nil {
		t.Fatal(err)
	}

	// iPXE: the first script, then the decision.
	if _, body := get(t, base+"/boot.ipxe"); !strings.Contains(string(body), base+"/boot?mac=") {
		t.Fatalf("bootstrap script:\n%s", body)
	}
	_, script := get(t, base+"/boot?mac=52-54-00-77-00-01&uuid=&serial=")
	if !strings.Contains(string(script), "kernel "+base+"/wimboot") {
		t.Fatalf("install script:\n%s", script)
	}
	if _, body := get(t, base+"/wimboot"); string(body) != "pretend wimboot" {
		t.Errorf("wimboot = %q", body)
	}
	dir := base + "/i/" + armed.Token + "/"
	if code, body := get(t, dir+"boot.wim"); code != 200 || !bytes.Equal(body, imagetest.BootWIM) {
		t.Errorf("boot.wim: %d, %d bytes", code, len(body))
	}
	_, start := get(t, dir+"kyro-start.cmd")
	wantUse := `net use %KYRO_SHARE% /user:kyro not-a-secret /TCPPORT:` + itoa(r.cfg.SMB.Port)
	if !strings.Contains(string(start), wantUse) {
		t.Errorf("start script lacks %q:\n%s", wantUse, start)
	}

	// Windows PE: what the start script does, with an SMB client.
	conn, err := smb.NewConnection(smb.Options{
		Host: "127.0.0.1", Port: r.cfg.SMB.Port,
		Initiator:             &spnego.NTLMInitiator{User: "kyro", Password: "not-a-secret"},
		RequireMessageSigning: true,
		DialTimeout:           5 * time.Second,
	})
	if err != nil {
		t.Fatalf("SMB: %v", err)
	}
	defer conn.Close()
	var install bytes.Buffer
	if err := conn.RetrieveFile(boot.ShareName, `win11\sources\install.wim`, 0, install.Write); err != nil {
		t.Fatalf("install.wim over SMB: %v", err)
	}
	if !bytes.Equal(install.Bytes(), imagetest.Files()["sources/install.wim"]) {
		t.Errorf("install.wim over SMB: %d bytes, content differs", install.Len())
	}
	// Setup asks for ei.cfg; the medium has EI.CFG.
	var eicfg bytes.Buffer
	if err := conn.RetrieveFile(boot.ShareName, `WIN11\Sources\ei.cfg`, 0, eicfg.Write); err != nil || eicfg.Len() == 0 {
		t.Errorf("ei.cfg over SMB: %v", err)
	}
	if err := conn.TreeConnect(boot.ShareName); err != nil {
		t.Fatalf("connecting the share: %v", err)
	}
	listing, err := conn.ListDirectory(boot.ShareName, "", "*")
	if err != nil || len(listing) != 1 || listing[0].Name != "win11" {
		t.Errorf("top of the share: %v, %v", listing, err)
	}
	payload := bytes.NewReader([]byte("x"))
	if err := conn.PutFile(boot.ShareName, `win11\new.txt`, 0, payload.Read); err == nil {
		t.Error("writing to the share succeeded")
	}

	// The machine restarts during the installation and asks again.
	if _, body := get(t, base+"/boot?mac=52-54-00-77-00-01"); strings.Contains(string(body), "kernel") {
		t.Fatalf("second boot got the installer:\n%s", body)
	}

	// The installed system reports back.
	resp, err := http.Post(base+"/api/v1/callback/"+armed.Token, "", nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("callback: %v, %v", resp, err)
	}
	resp.Body.Close()

	var out struct {
		Clients []boot.ClientView `json:"clients"`
	}
	_, raw := get(t, r.status+"/api/v1/clients")
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Clients) != 1 || out.Clients[0].State != "provisioned" {
		t.Errorf("status: %s (%v)", raw, err)
	}
	if code, page := get(t, r.status+"/"); code != 200 || !strings.Contains(string(page), "provisioned") {
		t.Errorf("status page: %d", code)
	}

	// The boot address has no status page, and the other way round.
	if code, _ := get(t, base+"/api/v1/clients"); code == 200 {
		t.Error("the client list is served on the boot address")
	}
	if code, _ := get(t, r.status+"/boot.ipxe"); code == 200 {
		t.Error("boot scripts are served on the status address")
	}

	if err := r.stop(); err != nil {
		t.Errorf("Run returned %v", err)
	}
	if _, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(r.cfg.HTTP.Port)), time.Second); err == nil {
		t.Error("the HTTP port is still open after Run returned")
	}
}

func TestImageAddedWhileRunning(t *testing.T) {
	r := run(t, nil)
	if _, err := images.Open(r.cfg.ImagesDir()).Add(imagetest.Write(t), "second", images.AddOptions{Link: true}); err != nil {
		t.Fatal(err)
	}
	key, _ := state.ParseKey("52:54:00:77:00:09")
	if _, err := r.state.Arm(key, "second", example.Name, false); err != nil {
		t.Fatal(err)
	}
	_, script := get(t, r.cfg.HTTPBase()+"/boot?mac=52-54-00-77-00-09")
	if !strings.Contains(string(script), "installing image second") {
		t.Errorf("an image added after the start cannot be installed:\n%s", script)
	}
}

func TestRunFails(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := func() config.Config {
		cfg := config.Default()
		cfg.Interface, cfg.Address, cfg.DataDir = "lo", "127.0.0.1", t.TempDir()
		cfg.HTTP.Port, cfg.SMB.Port, cfg.SMB.Password = freePort(t), freePort(t), "x"
		cfg.Status.Listen = ""
		off := false
		cfg.DHCP.Mode, cfg.TFTP.Enabled = "off", &off
		return cfg
	}
	t.Run("boot files missing", func(t *testing.T) {
		err := Run(context.Background(), base(), log, Options{SkipInterfaceCheck: true})
		if err == nil || !strings.Contains(err.Error(), "kyro bootfiles fetch") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("port taken", func(t *testing.T) {
		cfg := base()
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		cfg.HTTP.Port = l.Addr().(*net.TCPAddr).Port
		err = Run(context.Background(), cfg, log, Options{BootFiles: standIns(t, cfg.BootDir()), SkipInterfaceCheck: true})
		if err == nil || !strings.Contains(err.Error(), "boot HTTP") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("address not on the interface", func(t *testing.T) {
		cfg := base()
		cfg.Address = "192.0.2.77"
		err := Run(context.Background(), cfg, log, Options{BootFiles: standIns(t, cfg.BootDir())})
		if err == nil || !strings.Contains(err.Error(), "does not have the address") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("interface missing", func(t *testing.T) {
		cfg := base()
		cfg.Interface = "kyro-none"
		err := Run(context.Background(), cfg, log, Options{BootFiles: standIns(t, cfg.BootDir())})
		if err == nil || !strings.Contains(err.Error(), "kyro-none") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("damaged client record", func(t *testing.T) {
		cfg := base()
		os.MkdirAll(cfg.StateDir(), 0o750)
		os.WriteFile(filepath.Join(cfg.StateDir(), "mac-525400770001.json"), []byte("{"), 0o640)
		err := Run(context.Background(), cfg, log, Options{BootFiles: standIns(t, cfg.BootDir()), SkipInterfaceCheck: true})
		if err == nil || !strings.Contains(err.Error(), "client state") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("full DHCP", func(t *testing.T) {
		cfg := base()
		cfg.DHCP.Mode = "full"
		err := Run(context.Background(), cfg, log, Options{BootFiles: standIns(t, cfg.BootDir()), SkipInterfaceCheck: true})
		if err == nil || !strings.Contains(err.Error(), "not implemented") {
			t.Errorf("err = %v", err)
		}
	})
}

// With Secure Boot switched on, a UEFI client is given the shim, and the
// shim's request for "ipxe.efi" is answered with the signed iPXE. Without
// it, nothing of the Secure Boot chain is handed out.
func TestBootFileNames(t *testing.T) {
	loaded := map[string][]byte{
		"ipxe.efi":               []byte("unsigned"),
		"undionly.kpxe":          []byte("bios"),
		"wimboot":                []byte("wimboot"),
		bootfiles.ShimName:       []byte("shim"),
		bootfiles.SignedIPXEName: []byte("signed"),
	}
	for _, tc := range []struct {
		secure bool
		name   string
		want   string // empty: not found
	}{
		{false, "ipxe.efi", "unsigned"},
		{false, "undionly.kpxe", "bios"},
		{false, bootfiles.ShimName, ""},
		{false, bootfiles.SignedIPXEName, ""},
		{false, "wimboot", ""},
		{true, bootfiles.ShimName, "shim"},
		{true, "ipxe.efi", "signed"},
		{true, "/ipxe.efi", "signed"},
		{true, `\ipxe.efi`, "signed"},
		{true, bootfiles.SignedIPXEName, ""},
		{true, "undionly.kpxe", "bios"},
		{true, "wimboot", ""},
		{true, "revocations.efi", ""},
		{true, "../ipxe.efi", ""},
	} {
		got, ok := bootFile(loaded, tc.secure, tc.name)
		if string(got) != tc.want || ok != (tc.want != "") {
			t.Errorf("secure=%v %q: got %q, %v; want %q", tc.secure, tc.name, got, ok, tc.want)
		}
	}
	if got := uefiBootFile(true); got != bootfiles.ShimName {
		t.Errorf("boot file with Secure Boot: %q", got)
	}
	if got := uefiBootFile(false); got != "ipxe.efi" {
		t.Errorf("boot file without Secure Boot: %q", got)
	}
}
