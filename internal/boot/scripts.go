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
	"fmt"
	"regexp"
	"strings"
)

// ShareName is the name of the install share. Every image is a directory
// in it.
const ShareName = "install"

// The scripts in this file are sent to machines and run there. Whatever is
// written into them is checked first: an address, names of images and
// profiles, the share account and paths out of an ISO all have a limited
// set of characters, and none of them can end a line or start a command.

var (
	scriptWord = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	scriptPath = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	scriptBase = regexp.MustCompile(`^http://[A-Za-z0-9.-]+(:[0-9]{1,5})?$`)
)

// bootstrapScript is what iPXE loads first. It asks again with the
// identifiers of the machine, which DHCP did not carry.
func bootstrapScript(base string) string {
	return "#!ipxe\n" +
		"# Kyro: ask what this machine should do.\n" +
		"chain " + base + "/boot?mac=${netX/mac:hexhyp}&uuid=${uuid}&serial=${serial:uristring} || exit 1\n"
}

// localScript sends a machine to its own disk.
//
// "exit 1" and not "exit": a UEFI boot manager goes on to the next boot
// entry only if the one before it failed. After a plain "exit" some of them
// show their menu instead.
func localScript(why string) string {
	return "#!ipxe\n" +
		"echo Kyro: " + why + " Booting from the local disk.\n" +
		"exit 1\n"
}

// menuScript shows a machine that is not armed what it would have to be
// armed by. Nothing in the menu changes the machine.
func menuScript() string {
	return `#!ipxe
menu Kyro: this machine is not armed for an installation
item --gap -- MAC address    ${netX/mac}
item --gap -- UUID           ${uuid}
item --gap -- Serial number  ${serial}
item --gap --
item --gap -- To install it, arm it on the Kyro server and boot it again.
item --gap --
item local    Boot from the local disk
item reboot   Restart
choose --timeout 20000 --default local target || goto local
goto ${target}
:reboot
reboot
:local
exit 1
`
}

// installScript loads Windows PE with the files of one installation.
func installScript(base, token, image, profile string) (string, error) {
	if !scriptBase.MatchString(base) || !scriptWord.MatchString(token) || !scriptWord.MatchString(image) || !scriptWord.MatchString(profile) {
		return "", fmt.Errorf("refusing to write %q, %q, %q or %q into a boot script", base, token, image, profile)
	}
	dir := base + "/i/" + token + "/"
	return "#!ipxe\n" +
		"echo Kyro: installing image " + image + " with profile " + profile + "\n" +
		"kernel " + base + "/wimboot\n" +
		"initrd " + dir + "winpeshl.ini winpeshl.ini\n" +
		"initrd " + dir + "kyro-start.cmd kyro-start.cmd\n" +
		"initrd " + dir + "autounattend.xml autounattend.xml\n" +
		"initrd " + dir + "boot.wim boot.wim\n" +
		"boot || exit 1\n", nil
}

// winpeshl is the file that makes Windows PE run Kyro's start script in
// place of Setup.
const winpeshl = "[LaunchApps]\r\n%SYSTEMROOT%\\System32\\cmd.exe, /c %SYSTEMROOT%\\System32\\kyro-start.cmd\r\n"

// startParams is what the start script needs to know.
type startParams struct {
	Address  string // Kyro's address
	Port     int    // of the SMB server
	Username string
	Password string
	Image    string   // directory of the image in the share
	Install  string   // path of the install image inside the ISO
	Extras   []string // paths of ei.cfg and pid.txt inside the ISO
}

// startScript returns the batch file that runs in Windows PE: connect the
// install share, then start Setup from the RAM disk with the image on the
// share and the answer file that wimboot put next to this script.
func startScript(p startParams) ([]byte, error) {
	for _, w := range []string{p.Address, p.Username, p.Password, p.Image} {
		if !scriptWord.MatchString(w) {
			return nil, fmt.Errorf("refusing to write %q into the start script", w)
		}
	}
	for _, path := range append([]string{p.Install}, p.Extras...) {
		if !scriptPath.MatchString(path) {
			return nil, fmt.Errorf("refusing to write the path %q into the start script", path)
		}
	}
	if p.Port < 1 || p.Port > 65535 {
		return nil, fmt.Errorf("invalid SMB port %d", p.Port)
	}
	win := func(path string) string { return strings.ReplaceAll(path, "/", `\`) }

	netUse := `net use %KYRO_SHARE% /user:` + p.Username + ` ` + p.Password
	if p.Port != 445 {
		// Windows 11 24H2 and later can use another port. Older Windows
		// PE cannot, and fails here.
		netUse += fmt.Sprintf(" /TCPPORT:%d", p.Port)
	}
	lines := []string{
		`@echo off`,
		`rem Written by Kyro for one installation. Windows PE runs this in place of`,
		`rem Setup. It connects the install share and starts Setup from there.`,
		`set KYRO_SHARE=\\` + p.Address + `\` + ShareName,
		`set KYRO_SRC=%KYRO_SHARE%\` + p.Image,
		`echo.`,
		`echo Kyro: starting the network`,
		`wpeinit`,
		`wpeutil WaitForNetwork >nul`,
		`set KYRO_TRIES=0`,
		`:connect`,
		netUse + ` >nul 2>&1`,
		`if not errorlevel 1 goto connected`,
		`set /a KYRO_TRIES+=1`,
		`if %KYRO_TRIES% geq 40 goto noshare`,
		`ping -n 4 127.0.0.1 >nul`,
		`goto connect`,
		`:connected`,
		`echo Kyro: connected to %KYRO_SHARE%`,
	}
	// Setup started from the RAM disk does not look for these two on the
	// share. They decide the edition and the product key.
	for _, extra := range p.Extras {
		name := strings.ToLower(extra[strings.LastIndex(extra, "/")+1:])
		lines = append(lines, `copy /y %KYRO_SRC%\`+win(extra)+` X:\sources\`+name+` >nul`)
	}
	lines = append(lines,
		`echo Kyro: starting Windows Setup`,
		`X:\sources\setup.exe /installfrom:%KYRO_SRC%\`+win(p.Install)+` /unattend:X:\Windows\System32\autounattend.xml`,
		`if errorlevel 1 goto setupfailed`,
		`wpeutil reboot`,
		`goto :eof`,
		`:noshare`,
		`echo.`,
		`echo Kyro: could not connect to %KYRO_SHARE%. The last attempt said:`,
		netUse,
		`goto stopped`,
		`:setupfailed`,
		`echo.`,
		`echo Kyro: Windows Setup ended with an error. Its logs are in X:\Windows\Panther.`,
		`:stopped`,
		`echo Kyro: this installation has stopped. Arm the client again to retry.`,
		`cmd.exe /k`,
	)
	return []byte(strings.Join(lines, "\r\n") + "\r\n"), nil
}
