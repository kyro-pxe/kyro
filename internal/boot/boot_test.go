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

package boot

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kyro-pxe/kyro/internal/config"
	"github.com/kyro-pxe/kyro/internal/images"
	"github.com/kyro-pxe/kyro/internal/images/imagetest"
	"github.com/kyro-pxe/kyro/internal/profiles"
	"github.com/kyro-pxe/kyro/internal/profiles/example"
	"github.com/kyro-pxe/kyro/internal/state"
)

// fixture is a Service with one image and the example profile.
type fixture struct {
	t        testing.TB
	svc      *Service
	dir      string
	boot     http.Handler
	dropped  []netip.Addr
	logLines *bytes.Buffer
}

const (
	testMAC    = "52:54:00:77:00:01"
	testQuery  = "/boot?mac=52-54-00-77-00-01&uuid=4c4c4544-0042-3510-8054-b7c04f563432&serial=PF2ABCDE"
	otherQuery = "/boot?mac=52-54-00-77-00-02&uuid=&serial=OTHER"
)

func newFixture(t testing.TB) *fixture {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Interface, cfg.Address, cfg.DataDir, cfg.SMB.Password = "eth0", "192.168.77.1", dir, "not-a-secret"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	imgs := images.Open(cfg.ImagesDir())
	if _, err := imgs.Add(imagetest.Write(t), "win11", images.AddOptions{}); err != nil {
		t.Fatal(err)
	}
	profs := profiles.Open(cfg.ProfilesDir())
	imp, err := profiles.Parse(example.Bundle(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := profs.Add(imp, false); err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(cfg.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	fx := &fixture{t: t, dir: dir, logLines: &bytes.Buffer{}}
	tree := imgs.Tree()
	t.Cleanup(tree.Close)
	fx.svc = &Service{
		Config: cfg, State: st, Images: tree, Profiles: profs,
		Wimboot:   []byte("pretend wimboot"),
		Log:       slog.New(slog.NewTextHandler(fx.logLines, nil)),
		OnInstall: func(a netip.Addr) { fx.dropped = append(fx.dropped, a) },
	}
	fx.boot = fx.svc.Handler()
	return fx
}

// do sends a request to the boot handler as if from the given address.
func (fx *fixture) do(method, target, from string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, body)
	req.RemoteAddr = from + ":40000"
	rec := httptest.NewRecorder()
	fx.boot.ServeHTTP(rec, req)
	return rec
}

func (fx *fixture) get(target string) *httptest.ResponseRecorder {
	return fx.do("GET", target, "192.168.77.80", nil)
}

func (fx *fixture) arm(profile string) state.Client {
	fx.t.Helper()
	key, err := state.ParseKey(testMAC)
	if err != nil {
		fx.t.Fatal(err)
	}
	c, err := fx.svc.State.Arm(key, "win11", profile, false)
	if err != nil {
		fx.t.Fatal(err)
	}
	return c
}

func (fx *fixture) client() state.Client {
	key, _ := state.ParseKey(testMAC)
	c, _, _ := fx.svc.State.Get(key)
	return c
}

func isInstall(body string) bool { return strings.Contains(body, "kernel ") }
func isLocal(body string) bool {
	return strings.HasPrefix(body, "#!ipxe\n") && strings.HasSuffix(body, "exit 1\n") && !isInstall(body)
}

func TestBootstrap(t *testing.T) {
	fx := newFixture(t)
	body := fx.get("/boot.ipxe").Body.String()
	want := "chain http://192.168.77.1/boot?mac=${netX/mac:hexhyp}&uuid=${uuid}&serial=${serial:uristring} || exit 1\n"
	if !strings.HasPrefix(body, "#!ipxe\n") || !strings.HasSuffix(body, want) {
		t.Errorf("bootstrap script:\n%s", body)
	}
}

func TestUnarmedClientGetsNoInstaller(t *testing.T) {
	fx := newFixture(t)
	for i := 0; i < 3; i++ {
		rec := fx.get(testQuery)
		if rec.Code != 200 || !isLocal(rec.Body.String()) {
			t.Fatalf("request %d: %d\n%s", i, rec.Code, rec.Body.String())
		}
	}
	if c := fx.client(); c.State != state.Unknown || c.Boots != 3 {
		t.Errorf("record = %+v", c)
	}
	if len(fx.dropped) != 0 {
		t.Error("OnInstall was called for a client that is not installed")
	}
	// A request without identifiers is answered the same way.
	if body := fx.get("/boot").Body.String(); !isLocal(body) {
		t.Errorf("no identifiers:\n%s", body)
	}
}

func TestUnknownClientMenuIsOptIn(t *testing.T) {
	fx := newFixture(t)
	fx.svc.Config.UnknownClients = "menu"
	body := fx.get(testQuery).Body.String()
	if !strings.Contains(body, "menu Kyro") || isInstall(body) || !strings.Contains(body, "exit 1") {
		t.Errorf("menu:\n%s", body)
	}
	// The menu can do two things: boot the disk and restart.
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "item ") && !strings.HasPrefix(line, "item --gap") &&
			!strings.HasPrefix(line, "item local") && !strings.HasPrefix(line, "item reboot") {
			t.Errorf("unexpected menu entry: %s", line)
		}
	}
	for _, word := range []string{"chain", "kernel", "initrd", "sanboot", "imgfetch", "shell"} {
		if strings.Contains(body, word) {
			t.Errorf("the menu contains %q", word)
		}
	}
	// A client that is known and done does not get the menu.
	fx.arm(example.Name)
	fx.get(testQuery)
	if body := fx.get(testQuery).Body.String(); strings.Contains(body, "menu Kyro") {
		t.Errorf("an installing client got the menu:\n%s", body)
	}
}

func TestArmedClientIsInstalledOnce(t *testing.T) {
	fx := newFixture(t)
	armed := fx.arm(example.Name)

	// Another machine asks first and gets nothing.
	if body := fx.get(otherQuery).Body.String(); !isLocal(body) {
		t.Fatalf("the wrong machine got:\n%s", body)
	}

	body := fx.get(testQuery).Body.String()
	base := "http://192.168.77.1/i/" + armed.Token + "/"
	want := "#!ipxe\n" +
		"echo Kyro: installing image win11 with profile kyro-example\n" +
		"kernel http://192.168.77.1/wimboot\n" +
		"initrd " + base + "winpeshl.ini winpeshl.ini\n" +
		"initrd " + base + "kyro-start.cmd kyro-start.cmd\n" +
		"initrd " + base + "autounattend.xml autounattend.xml\n" +
		"initrd " + base + "boot.wim boot.wim\n" +
		"boot || exit 1\n"
	if body != want {
		t.Fatalf("install script:\n%s\nwant:\n%s", body, want)
	}
	if c := fx.client(); c.State != state.Installing {
		t.Fatalf("state = %s", c.State)
	}
	if len(fx.dropped) != 1 || fx.dropped[0].String() != "192.168.77.80" {
		t.Errorf("OnInstall calls: %v", fx.dropped)
	}

	// Setup restarts the machine; every further boot goes to the disk.
	for i := 0; i < 4; i++ {
		if body := fx.get(testQuery).Body.String(); !isLocal(body) {
			t.Fatalf("boot %d during the installation:\n%s", i, body)
		}
	}
	rec := fx.do("POST", "/api/v1/callback/"+armed.Token, "192.168.77.80", nil)
	if rec.Code != 200 {
		t.Fatalf("callback: %d %s", rec.Code, rec.Body.String())
	}
	if c := fx.client(); c.State != state.Provisioned {
		t.Fatalf("state after the callback = %s", c.State)
	}
	for i := 0; i < 3; i++ {
		if body := fx.get(testQuery).Body.String(); !isLocal(body) || !strings.Contains(body, "is installed") {
			t.Fatalf("boot %d after provisioning:\n%s", i, body)
		}
	}
}

func TestInstallationFiles(t *testing.T) {
	fx := newFixture(t)
	armed := fx.arm(example.Name)
	dir := "/i/" + armed.Token + "/"

	// Before the client has booted, the token opens nothing.
	if rec := fx.get(dir + "autounattend.xml"); rec.Code != 404 {
		t.Fatalf("answer file before the boot: %d", rec.Code)
	}
	fx.get(testQuery)

	if got := fx.get(dir + "winpeshl.ini").Body.String(); got != "[LaunchApps]\r\n%SYSTEMROOT%\\System32\\cmd.exe, /c %SYSTEMROOT%\\System32\\kyro-start.cmd\r\n" {
		t.Errorf("winpeshl.ini = %q", got)
	}

	start := fx.get(dir + "kyro-start.cmd").Body.String()
	for _, want := range []string{
		"@echo off\r\n",
		`set KYRO_SHARE=\\192.168.77.1\install` + "\r\n",
		`set KYRO_SRC=%KYRO_SHARE%\win11` + "\r\n",
		`net use %KYRO_SHARE% /user:kyro not-a-secret >nul 2>&1` + "\r\n",
		`copy /y %KYRO_SRC%\sources\EI.CFG X:\sources\ei.cfg >nul` + "\r\n",
		`X:\sources\setup.exe /installfrom:%KYRO_SRC%\sources\install.wim /unattend:X:\Windows\System32\autounattend.xml` + "\r\n",
	} {
		if !strings.Contains(start, want) {
			t.Errorf("start script lacks %q", want)
		}
	}
	if strings.Contains(strings.ReplaceAll(start, "\r\n", ""), "\n") || strings.Contains(start, "TCPPORT") {
		t.Errorf("start script has bare line feeds or a port option:\n%s", start)
	}

	answer := fx.get(dir + "autounattend.xml").Body.String()
	for _, want := range []string{
		"<ComputerName>KYRO-00770001</ComputerName>",
		"-X POST http://192.168.77.1/api/v1/callback/" + armed.Token + "</CommandLine>",
	} {
		if !strings.Contains(answer, want) {
			t.Errorf("answer file lacks %q", want)
		}
	}
	if strings.Contains(answer, "{{") {
		t.Error("answer file still has a token in it")
	}

	rec := fx.get(dir + "boot.wim")
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), imagetest.BootWIM) {
		t.Errorf("boot.wim: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	// iPXE fetches large files in ranges when a transfer is interrupted.
	req := httptest.NewRequest("GET", dir+"boot.wim", nil)
	req.RemoteAddr = "192.168.77.80:1"
	req.Header.Set("Range", "bytes=100-199")
	rr := httptest.NewRecorder()
	fx.boot.ServeHTTP(rr, req)
	if rr.Code != 206 || !bytes.Equal(rr.Body.Bytes(), imagetest.BootWIM[100:200]) {
		t.Errorf("range request: %d, %d bytes", rr.Code, rr.Body.Len())
	}

	if rec := fx.get("/wimboot"); rec.Body.String() != "pretend wimboot" {
		t.Errorf("wimboot = %q", rec.Body.String())
	}

	// Nothing else is there, and nothing under another token.
	for _, path := range []string{
		dir + "install.wim", dir + "../boot.ipxe", dir, dir + "sources/boot.wim", dir + "profile.json",
		"/i/" + strings.Repeat("0", 32) + "/autounattend.xml", "/i/x/autounattend.xml", "/i//autounattend.xml",
		"/images/win11.iso", "/profiles/kyro-example/autounattend.xml", "/state/", "/", "/api/v1/clients", "/healthz",
	} {
		if rec := fx.get(path); rec.Code == 200 {
			t.Errorf("GET %s: 200 with %d bytes", path, rec.Body.Len())
		}
	}

	// Once the installation is over, its files are gone.
	fx.do("POST", "/api/v1/callback/"+armed.Token, "192.168.77.80", nil)
	for _, file := range []string{"autounattend.xml", "kyro-start.cmd", "boot.wim", "winpeshl.ini"} {
		if rec := fx.get(dir + file); rec.Code != 404 {
			t.Errorf("%s after the installation: %d", file, rec.Code)
		}
	}
}

func TestCallback(t *testing.T) {
	fx := newFixture(t)
	armed := fx.arm(example.Name)
	path := "/api/v1/callback/" + armed.Token

	// Armed, not yet booted: there is nothing to report.
	if rec := fx.do("POST", path, "192.168.77.80", nil); rec.Code != 404 {
		t.Fatalf("callback before the boot: %d", rec.Code)
	}
	fx.get(testQuery)

	for _, bad := range []string{"/api/v1/callback/", "/api/v1/callback/x", "/api/v1/callback/" + strings.Repeat("f", 32), path + "/extra", path + "0"} {
		if rec := fx.do("POST", bad, "192.168.77.80", nil); rec.Code == 200 {
			t.Errorf("POST %s: 200", bad)
		}
	}
	if rec := fx.do("GET", path, "192.168.77.80", nil); rec.Code != 405 {
		t.Errorf("GET on the callback: %d", rec.Code)
	}
	if fx.client().State != state.Installing {
		t.Fatal("a wrong request changed the state")
	}

	// A large body is not read, and does not get in the way.
	big := strings.NewReader(strings.Repeat("x", 1<<20))
	if rec := fx.do("POST", path, "192.168.77.80", big); rec.Code != 200 {
		t.Fatalf("callback: %d", rec.Code)
	}
	if big.Len() < 1<<19 {
		t.Errorf("the server read %d bytes of a body it does not need", 1<<20-big.Len())
	}
	c := fx.client()
	if c.State != state.Provisioned || c.FinishedAt == nil {
		t.Fatalf("after the callback: %+v", c)
	}
	// Curl retries; a second report is fine and changes nothing.
	finished := *c.FinishedAt
	if rec := fx.do("POST", path, "192.168.77.80", nil); rec.Code != 200 {
		t.Errorf("second callback: %d", rec.Code)
	}
	if c := fx.client(); !c.FinishedAt.Equal(finished) {
		t.Error("the second callback changed the record")
	}
}

func TestCallbackIsRateLimited(t *testing.T) {
	fx := newFixture(t)
	codes := map[int]int{}
	for i := 0; i < 60; i++ {
		codes[fx.do("POST", "/api/v1/callback/"+strings.Repeat("a", 32), "192.168.77.99", nil).Code]++
	}
	if codes[429] < 30 || codes[404] > 25 {
		t.Errorf("status codes of 60 guesses from one address: %v", codes)
	}
	// Another address is not affected.
	armed := fx.arm(example.Name)
	fx.get(testQuery)
	if rec := fx.do("POST", "/api/v1/callback/"+armed.Token, "192.168.77.80", nil); rec.Code != 200 {
		t.Errorf("callback from another address: %d", rec.Code)
	}
}

func TestBootIsRateLimited(t *testing.T) {
	fx := newFixture(t)
	// One address invents machines. After a while it is no longer
	// recorded, and it still only ever gets the local disk.
	for i := 0; i < 80; i++ {
		body := fx.do("GET", "/boot?mac=02-00-00-00-00-"+hex2(i), "192.168.77.99", nil).Body.String()
		if !isLocal(body) {
			t.Fatalf("request %d:\n%s", i, body)
		}
	}
	list, _ := fx.svc.State.List()
	if len(list) > 35 {
		t.Errorf("%d records written for one address in a burst", len(list))
	}
}

func hex2(i int) string {
	const d = "0123456789abcdef"
	return string([]byte{d[i>>4&15], d[i&15]})
}

func TestInstallationThatCannotStartFails(t *testing.T) {
	needsSerial := `<?xml version="1.0"?><unattend xmlns="urn:schemas-microsoft-com:unattend"><a>{{serial}}</a></unattend>`
	tests := []struct {
		name    string
		breakIt func(fx *fixture)
		query   string
		want    string
	}{
		{"profile removed", func(fx *fixture) { fx.svc.Profiles.Remove(example.Name) }, testQuery, "no such profile"},
		{"image removed", func(fx *fixture) { images.Open(fx.svc.Config.ImagesDir()).Remove("win11") }, testQuery, "no such image"},
		{"answer file changed on disk", func(fx *fixture) {
			os.WriteFile(filepath.Join(fx.svc.Config.ProfilesDir(), example.Name, "autounattend.xml"), []byte("<unattend/>"), 0o640)
		}, testQuery, "changed after it was imported"},
		{"client sent no serial that the profile needs", func(fx *fixture) {
			imp, err := profiles.Parse([]byte(needsSerial), example.Name)
			if err != nil {
				fx.t.Fatal(err)
			}
			if err := fx.svc.Profiles.Add(imp, true); err != nil {
				fx.t.Fatal(err)
			}
		}, "/boot?mac=52-54-00-77-00-01&serial=To+be+filled+by+O.E.M.", "{{serial}}"},
		{"install share switched off", func(fx *fixture) { fx.svc.Config.SMB.Mode = "off" }, testQuery, "switched off"},
	}
	for _, tt := range tests {
		fx := newFixture(t)
		armed := fx.arm(example.Name)
		tt.breakIt(fx)
		body := fx.get(tt.query).Body.String()
		if !isLocal(body) {
			t.Errorf("%s: the client got:\n%s", tt.name, body)
		}
		c := fx.client()
		if c.State != state.Failed || !strings.Contains(c.Failure, tt.want) {
			t.Errorf("%s: state %s, failure %q", tt.name, c.State, c.Failure)
		}
		// It does not try again by itself.
		if body := fx.get(tt.query).Body.String(); !isLocal(body) {
			t.Errorf("%s: second boot:\n%s", tt.name, body)
		}
		if rec := fx.get("/i/" + armed.Token + "/autounattend.xml"); rec.Code != 404 {
			t.Errorf("%s: answer file of a failed installation: %d", tt.name, rec.Code)
		}
	}
}

func TestDamagedStateMeansLocalDisk(t *testing.T) {
	fx := newFixture(t)
	fx.arm(example.Name)
	os.WriteFile(filepath.Join(fx.svc.Config.StateDir(), "mac-525400770001.json"), []byte("{broken"), 0o640)
	if body := fx.get(testQuery).Body.String(); !isLocal(body) {
		t.Errorf("with a damaged record the client got:\n%s", body)
	}
}

func TestExpire(t *testing.T) {
	fx := newFixture(t)
	fx.arm(example.Name)
	fx.get(testQuery)
	now := time.Now()
	fx.svc.State.Now = func() time.Time { return now.Add(4 * time.Hour) }
	fx.svc.Expire()
	if c := fx.client(); c.State != state.Failed {
		t.Errorf("state = %s", c.State)
	}
	if !strings.Contains(fx.logLines.String(), "installation failed") {
		t.Error("the failure was not logged")
	}
}

func TestStartScriptPort(t *testing.T) {
	good := startParams{Address: "10.0.0.1", Port: 4445, Username: "kyro", Password: "pw", Image: "win11", Install: "sources/install.esd", Extras: []string{"sources/ei.cfg", "sources/PID.txt"}}
	out, err := startScript(good)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`net use %KYRO_SHARE% /user:kyro pw /TCPPORT:4445 >nul 2>&1`,
		`/installfrom:%KYRO_SRC%\sources\install.esd `,
		`copy /y %KYRO_SRC%\sources\PID.txt X:\sources\pid.txt >nul`,
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("start script lacks %q", want)
		}
	}
}

// TestScriptsRefuseUnsafeValues: nothing that could end a line or start a
// command gets into a script, whatever the configuration or an ISO says.
func TestScriptsRefuseUnsafeValues(t *testing.T) {
	good := startParams{Address: "10.0.0.1", Port: 445, Username: "kyro", Password: "pw", Image: "win11", Install: "sources/install.wim"}
	for _, bad := range []string{"", "a b", "a&calc", "a|b", "a%PATH%", "a^b", "a\r\nformat c:", `a"b`, "a>b", "..", "-x", "a;b", "a\x00"} {
		for name, edit := range map[string]func(*startParams){
			"address":  func(p *startParams) { p.Address = bad },
			"username": func(p *startParams) { p.Username = bad },
			"password": func(p *startParams) { p.Password = bad },
			"image":    func(p *startParams) { p.Image = bad },
			"install":  func(p *startParams) { p.Install = "sources/" + bad },
			"extra":    func(p *startParams) { p.Extras = []string{"sources/" + bad} },
		} {
			if bad == ".." && (name == "install" || name == "extra") {
				continue // a path element of two dots is caught when the ISO is inspected
			}
			if bad == "-x" && (name == "install" || name == "extra") {
				continue // a file name may start with a hyphen
			}
			p := good
			edit(&p)
			if out, err := startScript(p); err == nil {
				t.Errorf("%s %q accepted:\n%s", name, bad, out)
			}
		}
		if s, err := installScript("http://10.0.0.1", strings.Repeat("a", 32), bad, "p"); err == nil && bad != ".." {
			t.Errorf("image %q accepted in a boot script:\n%s", bad, s)
		}
		if s, err := installScript("http://10.0.0.1", bad, "win11", "p"); err == nil && bad != ".." {
			t.Errorf("token %q accepted in a boot script:\n%s", bad, s)
		}
	}
	if _, err := installScript("http://10.0.0.1/x y", strings.Repeat("a", 32), "win11", "p"); err == nil {
		t.Error("an odd base URL was accepted")
	}
}

func TestStatus(t *testing.T) {
	fx := newFixture(t)
	status := fx.svc.StatusHandler()
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		status.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	if body := get("/").Body.String(); !strings.Contains(body, "No client has booted") {
		t.Errorf("empty status page:\n%s", body)
	}
	if got := strings.TrimSpace(get("/api/v1/clients").Body.String()); got != `{"clients":[]}` {
		t.Errorf("empty list = %s", got)
	}

	armed := fx.arm(example.Name)
	fx.get(testQuery)
	fx.get(otherQuery)
	fx.svc.State.Fail(armed.Token, `<script>alert(1)</script>`)

	page := get("/").Body.String()
	for _, want := range []string{"mac:52:54:00:77:00:01", "failed", "win11", "kyro-example", "PF2ABCDE", "mac:52:54:00:77:00:02", "unknown", "&lt;script&gt;"} {
		if !strings.Contains(page, want) {
			t.Errorf("status page lacks %q", want)
		}
	}
	if strings.Contains(page, "<script>") {
		t.Error("the status page does not escape what it shows")
	}

	var out struct {
		Clients []ClientView `json:"clients"`
	}
	raw := get("/api/v1/clients").Body.String()
	if err := json.Unmarshal([]byte(raw), &out); err != nil || len(out.Clients) != 2 {
		t.Fatalf("API: %v\n%s", err, raw)
	}
	if c := out.Clients[0]; c.Client != "mac:52:54:00:77:00:01" || c.State != "failed" || c.MAC != testMAC || c.Boots != 1 {
		t.Errorf("first client = %+v", c)
	}
	// The token opens the files of an installation; it is shown nowhere.
	if strings.Contains(page, armed.Token) || strings.Contains(raw, armed.Token) || strings.Contains(raw, "token") {
		t.Error("the status output contains the token")
	}

	// Read-only, and nothing of the boot side.
	for _, path := range []string{"/boot", "/boot.ipxe", "/wimboot", "/i/" + armed.Token + "/autounattend.xml", "/api/v1/callback/" + armed.Token} {
		if rec := get(path); rec.Code == 200 {
			t.Errorf("status handler serves %s", path)
		}
	}
	rec := httptest.NewRecorder()
	status.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/clients", nil))
	if rec.Code != 405 {
		t.Errorf("POST to the status API: %d", rec.Code)
	}
	if get("/healthz").Code != 200 {
		t.Error("healthz")
	}
}

var scriptLine = regexp.MustCompile(`^[ -~]*$`)

// FuzzBoot sends arbitrary boot requests while one machine is armed. Only
// that machine may get the installer, only once, and whatever is sent, the
// answer is a script made of Kyro's own words.
func FuzzBoot(f *testing.F) {
	f.Add("52-54-00-77-00-01", "4c4c4544-0042-3510-8054-b7c04f563432", "PF2ABCDE")
	f.Add("52-54-00-77-00-02", "", "")
	f.Add("525400770001\nkernel http://evil/x", "${uuid}", "a b&c")
	f.Add("", "", "")
	f.Add(strings.Repeat("A", 500), "x", "y")
	f.Fuzz(func(t *testing.T, mac, uuid, serial string) {
		fx := newFixture(t)
		armed := fx.arm(example.Name)
		q := url.Values{"mac": {mac}, "uuid": {uuid}, "serial": {serial}}
		first := fx.get("/boot?" + q.Encode())
		second := fx.get("/boot?" + q.Encode())

		id := state.NewIdentity(mac, uuid, serial)
		for i, rec := range []*httptest.ResponseRecorder{first, second} {
			body := rec.Body.String()
			if rec.Code != 200 || !strings.HasPrefix(body, "#!ipxe\n") {
				t.Fatalf("answer %d is not a script: %d %q", i, rec.Code, body)
			}
			for _, line := range strings.Split(body, "\n") {
				if !scriptLine.MatchString(line) {
					t.Fatalf("answer %d has a line with odd characters: %q", i, line)
				}
			}
			wantInstall := i == 0 && id.MAC == "525400770001"
			if isInstall(body) != wantInstall {
				t.Fatalf("answer %d to mac=%q: install=%v, want %v\n%s", i, mac, isInstall(body), wantInstall, body)
			}
			if isInstall(body) && !strings.Contains(body, "/i/"+armed.Token+"/") {
				t.Fatalf("install script without the token:\n%s", body)
			}
		}
	})
}

// FuzzCallback sends arbitrary tokens to the callback while one client is
// installing. Only its token may change anything.
func FuzzCallback(f *testing.F) {
	f.Add(strings.Repeat("a", 32), "")
	f.Add("../../boot", "x")
	f.Add("%2e%2e/x", strings.Repeat("y", 5000))
	f.Fuzz(func(t *testing.T, token, body string) {
		fx := newFixture(t)
		armed := fx.arm(example.Name)
		fx.get(testQuery)
		target := "/api/v1/callback/" + url.PathEscape(token)
		req, err := http.NewRequest("POST", target, strings.NewReader(body))
		if err != nil {
			return
		}
		req.RemoteAddr = "192.168.77.80:1"
		rec := httptest.NewRecorder()
		fx.boot.ServeHTTP(rec, req)
		if token == armed.Token {
			return
		}
		if rec.Code == 200 {
			t.Fatalf("token %q was accepted", token)
		}
		if fx.client().State != state.Installing {
			t.Fatalf("token %q changed the state to %s", token, fx.client().State)
		}
	})
}
