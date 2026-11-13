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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kyro-pxe/kyro/internal/images/imagetest"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"version", []string{"version"}, 0, "kyro " + version, ""},
		{"help", []string{"help"}, 0, "Usage:", ""},
		{"help lists the commands", []string{"help"}, 0, "arm <client> --image X --profile Y", ""},
		{"help says what selection is", []string{"help"}, 0, "it does not authenticate", ""},
		{"no command", nil, 2, "", "Usage:"},
		{"unknown command", []string{"bogus"}, 2, "", `unknown command "bogus"`},
		{"share without flags", []string{"share"}, 2, "", "-listen, -root and -password are required"},
		{"share with an unknown flag", []string{"share", "-bogus"}, 2, "", "flag provided but not defined"},
		{"share with an extra argument", []string{"share", "-listen", "127.0.0.1:0", "-root", ".", "-password", "x", "extra"}, 2, "", "unexpected argument"},
		{"share with a missing directory", []string{"share", "-listen", "127.0.0.1:0", "-root", "/nonexistent/kyro", "-password", "x"}, 1, "", "no such file"},
		{"share with a bad password", []string{"share", "-listen", "127.0.0.1:0", "-root", ".", "-password", "a b"}, 2, "", "invalid password"},
		{"share with a bad share name", []string{"share", "-listen", "127.0.0.1:0", "-root", ".", "-password", "x", "-name", "a b"}, 2, "", "invalid share name"},
		{"share with a bad address", []string{"share", "-listen", "nonsense", "-root", ".", "-password", "x"}, 1, "", "kyro share:"},
		{"share help", []string{"share", "-h"}, 0, "", "Experimental"},
		{"no configuration", []string{"clients", "--config", "/nonexistent/kyro.yaml"}, 1, "", "there is no configuration at /nonexistent/kyro.yaml"},
		{"image without a subcommand", []string{"image"}, 2, "", "kyro image add"},
		{"profile with an unknown subcommand", []string{"profile", "edit"}, 2, "", `unknown subcommand "edit"`},
		{"arm help", []string{"arm", "-h"}, 0, "", "This works once"},
		{"unknown flag", []string{"clients", "--bogus"}, 2, "", "flag provided but not defined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), tt.args, &stdout, &stderr); code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr: %s)", code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

// cli runs commands against a configuration in a temporary directory.
type cli struct {
	t      *testing.T
	config string
	data   string
}

func newCLI(t *testing.T) *cli {
	t.Helper()
	dir := t.TempDir()
	c := &cli{t: t, config: filepath.Join(dir, "config.yaml"), data: filepath.Join(dir, "data")}
	cfg := "interface: eth0\naddress: 192.168.77.1\ndata_dir: " + c.data + "\nsmb:\n  password: not-a-secret\n"
	if err := os.WriteFile(c.config, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return c
}

// run returns the exit code and what the command printed.
func (c *cli) run(args ...string) (code int, stdout, stderr string) {
	c.t.Helper()
	c.t.Setenv("KYRO_CONFIG", c.config)
	var out, errOut bytes.Buffer
	code = run(context.Background(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// ok runs a command that must succeed and returns its output.
func (c *cli) ok(args ...string) string {
	c.t.Helper()
	code, out, errOut := c.run(args...)
	if code != 0 {
		c.t.Fatalf("kyro %s: exit code %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, out, errOut)
	}
	return out
}

// fails runs a command that must fail and returns what it printed.
func (c *cli) fails(wantCode int, args ...string) string {
	c.t.Helper()
	code, out, errOut := c.run(args...)
	if code != wantCode {
		c.t.Fatalf("kyro %s: exit code %d, want %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, wantCode, out, errOut)
	}
	return out + errOut
}

func wants(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("output lacks %q:\n%s", w, got)
		}
	}
}

func TestImageCommands(t *testing.T) {
	c := newCLI(t)
	iso := imagetest.Write(t)

	wants(t, c.ok("image", "list"), "No images")
	out := c.ok("image", "add", iso, "--name", "win11")
	wants(t, out, "Image win11 added", "sha256 ", "1  Windows 11 Test Edition (build 26100, x64)", "2  Windows 11 Other Edition")
	// The name defaults to the file name; flags may come first.
	wants(t, c.ok("image", "add", "--link", iso), "Image windows-standin added")
	wants(t, c.ok("image", "list"), "NAME", "win11", "windows-standin", "1: Windows 11 Test Edition")

	wants(t, c.fails(1, "image", "add", iso, "--name", "win11"), `an image called "win11" exists`)
	wants(t, c.fails(1, "image", "add", iso, "--name", "Not Valid"), "image name")
	wants(t, c.fails(1, "image", "add", c.config, "--name", "x"), "not a UDF image")
	wants(t, c.fails(1, "image", "add", "/nonexistent.iso"), "no such file")
	wants(t, c.fails(2, "image", "add"), "name one ISO file")

	wants(t, c.ok("image", "remove", "windows-standin"), "removed")
	wants(t, c.fails(1, "image", "remove", "windows-standin"), "there is no image")
	if _, err := os.Stat(iso); err != nil {
		t.Errorf("removing a linked image deleted the ISO: %v", err)
	}
}

const bareAnswer = `<?xml version="1.0"?>
<unattend xmlns="urn:schemas-microsoft-com:unattend"><settings pass="oobeSystem"><a b="{{mac}}">{{hostname}}</a></settings></unattend>`

func TestProfileCommands(t *testing.T) {
	c := newCLI(t)
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	wants(t, c.ok("profile", "list"), "No profiles")

	out := c.ok("profile", "import", "--example")
	wants(t, out,
		"Profile kyro-example imported.",
		"TEST FIXTURE",
		"DISKS: treated as wiping a disk",
		"needs --confirm-wipe",
		"WARNING from the bundle: Wipes disk 0 without asking.",
		`password "kyro-test-only"`,
		"Filled in per client: {{callback_url}}, {{mac}}",
		"Answer files can wipe disks and contain passwords.",
	)
	wants(t, c.fails(1, "profile", "import", "--example"), "exists; use --replace")
	c.ok("profile", "import", "--example", "--replace")

	// A bare answer file gets its name from the file, and the notes about
	// what will not be replaced.
	out = c.ok("profile", "import", write("Office Laptops.xml", bareAnswer))
	wants(t, out, "Profile office-laptops imported.", "bare answer file", "{{mac}} is in an attribute", "{{hostname}} is not a Kyro token")
	c.ok("profile", "import", write("x.xml", bareAnswer), "--name", "second")

	harmless := `{"bundle_version":0,"name":"harmless","wipes_disk":false,"answer_file":"<unattend xmlns=\"urn:schemas-microsoft-com:unattend\"/>"}`
	out = c.ok("profile", "import", write("h.kyro.json", harmless))
	wants(t, out, "leaves disks alone", "carries no warnings. That is not a statement that it is harmless")

	out = c.ok("profile", "list")
	wants(t, out, "NAME", "kyro-example", "office-laptops", "second", "harmless")
	out = c.ok("profile", "show", "kyro-example")
	wants(t, out, "Profile kyro-example", "from a bundle", "sha256 ", "DISKS")
	if xml := c.ok("profile", "show", "second", "--xml"); xml != bareAnswer {
		t.Errorf("show --xml changed the answer file:\n%s", xml)
	}

	// What must not get in.
	for name, content := range map[string]string{
		"doctype.xml": `<!DOCTYPE unattend [<!ENTITY xxe SYSTEM "file:///etc/passwd">]><unattend xmlns="urn:schemas-microsoft-com:unattend">&xxe;</unattend>`,
		"notxml.xml":  "hello",
		"future.json": `{"bundle_version":7}`,
		"huge.xml":    "<unattend xmlns=\"urn:schemas-microsoft-com:unattend\">" + strings.Repeat("x", 3<<20) + "</unattend>",
	} {
		wants(t, c.fails(1, "profile", "import", write(name, content), "--name", "bad"), "not imported")
	}
	wants(t, c.fails(1, "profile", "show", "bad"), "there is no profile")
	wants(t, c.fails(1, "profile", "import", write("y.xml", bareAnswer), "--name", "../escape"), "cannot be a profile name")
	wants(t, c.fails(2, "profile", "import"), "name one file, or use --example")

	wants(t, c.ok("profile", "remove", "second"), "removed")
	wants(t, c.fails(1, "profile", "remove", "second"), "there is no profile")
}

func TestArmNeedsConfirmationToWipe(t *testing.T) {
	c := newCLI(t)
	c.ok("image", "add", imagetest.Write(t), "--name", "win11")
	c.ok("profile", "import", "--example")
	mac := "52:54:00:77:00:01"

	out := c.fails(1, "arm", mac, "--image", "win11", "--profile", "kyro-example")
	wants(t, out, "Not armed.", "--confirm-wipe", "WARNING from the bundle: Wipes disk 0 without asking.", "would be lost")
	// Nothing was recorded.
	wants(t, c.ok("clients"), "No clients yet")

	out = c.ok("arm", mac, "--image", "win11", "--profile", "kyro-example", "--confirm-wipe")
	wants(t, out,
		"Armed: mac:52:54:00:77:00:01 will be installed with image win11 and profile kyro-example",
		"once",
		"WARNING from the bundle",
		"That is not\nauthentication",
	)
	wants(t, c.ok("clients"), "CLIENT", "mac:52:54:00:77:00:01", "armed", "win11", "kyro-example")

	var list struct {
		Clients []map[string]any `json:"clients"`
	}
	raw := c.ok("clients", "--json")
	if err := json.Unmarshal([]byte(raw), &list); err != nil || len(list.Clients) != 1 || list.Clients[0]["state"] != "armed" {
		t.Errorf("clients --json: %v\n%s", err, raw)
	}
	if strings.Contains(raw, "token") {
		t.Error("clients --json shows the token")
	}

	// A bare answer file always needs the confirmation; a bundle that
	// declares itself harmless does not.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bare.xml"), []byte(bareAnswer), 0o644)
	os.WriteFile(filepath.Join(dir, "h.json"), []byte(`{"bundle_version":0,"name":"harmless","wipes_disk":false,"answer_file":"<unattend xmlns=\"urn:schemas-microsoft-com:unattend\"/>"}`), 0o644)
	c.ok("profile", "import", filepath.Join(dir, "bare.xml"))
	c.ok("profile", "import", filepath.Join(dir, "h.json"))
	wants(t, c.fails(1, "arm", "PF2ABCDE", "--image", "win11", "--profile", "bare"), "Not armed.", "bare answer file")
	wants(t, c.ok("arm", "PF2ABCDE", "--image", "win11", "--profile", "harmless"), "Armed: serial:PF2ABCDE")

	wants(t, c.ok("disarm", mac), "is disarmed")
	wants(t, c.fails(1, "disarm", mac), "is not armed")
	wants(t, c.ok("disarm", "PF2ABCDE", "--forget"), "forgotten")
	wants(t, c.ok("clients"), "No clients yet")
}

func TestArmRejects(t *testing.T) {
	c := newCLI(t)
	c.ok("image", "add", imagetest.Write(t), "--name", "win11")
	c.ok("profile", "import", "--example")
	tests := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"arm"}, 2, "name one client"},
		{[]string{"arm", "52:54:00:77:00:01"}, 2, "--image and --profile are required"},
		{[]string{"arm", "52:54:00:77:00:01", "--image", "win11"}, 2, "--image and --profile are required"},
		{[]string{"arm", "52:54:00:77:00:01", "extra", "--image", "win11", "--profile", "kyro-example"}, 2, "name one client"},
		{[]string{"arm", "To be filled by O.E.M.", "--image", "win11", "--profile", "kyro-example", "--confirm-wipe"}, 2, "placeholder"},
		{[]string{"arm", "00:00:00:00:00:00", "--image", "win11", "--profile", "kyro-example", "--confirm-wipe"}, 2, "placeholder"},
		{[]string{"arm", "a b c", "--image", "win11", "--profile", "kyro-example", "--confirm-wipe"}, 2, "not a MAC address"},
		{[]string{"arm", "52:54:00:77:00:01", "--image", "missing", "--profile", "kyro-example", "--confirm-wipe"}, 1, `no image called "missing"`},
		{[]string{"arm", "52:54:00:77:00:01", "--image", "win11", "--profile", "missing", "--confirm-wipe"}, 1, `no profile called "missing"`},
		{[]string{"disarm"}, 2, "name one client"},
	}
	for _, tt := range tests {
		wants(t, c.fails(tt.code, tt.args...), tt.want)
	}
	wants(t, c.ok("clients"), "No clients yet")

	// A profile whose answer file was changed on disk cannot be armed.
	os.WriteFile(filepath.Join(c.data, "profiles", "kyro-example", "autounattend.xml"), []byte("<unattend/>"), 0o640)
	wants(t, c.fails(1, "arm", "52:54:00:77:00:01", "--image", "win11", "--profile", "kyro-example", "--confirm-wipe"), "changed after it was imported")
}

func TestBootfilesVerify(t *testing.T) {
	c := newCLI(t)
	wants(t, c.fails(1, "bootfiles", "verify"), "wimboot is missing", "ipxe.efi is missing", "kyro bootfiles fetch")
	wants(t, c.fails(1, "bootfiles", "fetch", "--from", t.TempDir()), "no such file")
	wants(t, c.fails(2, "bootfiles"), "fetch")
	// The real files, if they are at hand.
	if from := os.Getenv("KYRO_TEST_BOOTFILES"); from != "" {
		wants(t, c.ok("bootfiles", "fetch", "--from", from), "wimboot installed", "ipxe.efi installed")
		wants(t, c.ok("bootfiles", "verify"), "have the pinned checksums")
	}
}

func TestServeFailsWithoutBootFiles(t *testing.T) {
	c := newCLI(t)
	// The interface of the test configuration does not exist either; both
	// are reasons not to start, and neither may hang.
	out := c.fails(1, "serve")
	if !strings.Contains(out, "eth0") && !strings.Contains(out, "bootfiles") {
		t.Errorf("serve: %s", out)
	}
}
